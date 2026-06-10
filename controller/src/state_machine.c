#include <stddef.h>
#include <stdint.h>
#include <stdbool.h>
#include <string.h>

#include "config.h"
#include "state_machine.h"

/* ── safety constants ─────────────────────────────────────────────────────── */

/* Link-loss: 10 s without a command or heartbeat.
   Brain sends heartbeats every 2 s — the timeout must be well above that to
   absorb OS scheduling jitter and TCP stack delays without spurious faults. */
#define LINK_TIMEOUT_TICKS    (TICK_HZ * 10U)

/* G-5500 duty cycle: 3 min on, 15 min rest */
#define DUTY_MAX_ON_TICKS     (TICK_HZ * 60U * 3U)
#define DUTY_MIN_REST_TICKS   (TICK_HZ * 60U * 15U)

/* ── helpers ──────────────────────────────────────────────────────────────── */

static bool is_faulted(const sm_ctx_t *ctx)
{
    return ctx->state == SM_STATE_FAULT_LINK_LOST    ||
           ctx->state == SM_STATE_FAULT_LIMIT        ||
           ctx->state == SM_STATE_FAULT_DUTY_CYCLE   ||
           ctx->state == SM_STATE_FAULT_ADC_INVALID;
}

static void enter_fault(sm_ctx_t *ctx, sm_state_t fault)
{
    ctx->state  = fault;
    ctx->az_cmd = SM_AZ_STOP;
    ctx->el_cmd = SM_EL_STOP;
}

/* ── public API ───────────────────────────────────────────────────────────── */

void sm_init(sm_ctx_t *ctx)
{
    memset(ctx, 0, sizeof(*ctx));
    ctx->state  = SM_STATE_IDLE;
    ctx->az_min = 0.0f;  ctx->az_max = 1.0f;
    ctx->el_min = 0.0f;  ctx->el_max = 1.0f;
}

void sm_push_command(sm_ctx_t *ctx, const sm_command_t *cmd)
{
    /* Emergency stop always wins — no priority check. */
    if (cmd->type == CMD_TYPE_EMERGENCY_STOP) {
        ctx->has_command = true;
        ctx->pending_cmd = *cmd;
        return;
    }
    if (!ctx->has_command ||
        cmd->priority >= ctx->pending_cmd.priority ||
        ctx->pending_cmd.type == CMD_TYPE_HEARTBEAT) {
        ctx->has_command = true;
        ctx->pending_cmd = *cmd;
    }
}

sm_output_t sm_tick(sm_ctx_t *ctx, const sm_input_t *in)
{
    /* ── 1. process pending command (before watchdog so heartbeat wins) ─── */
    if (ctx->has_command) {
        const sm_command_t *cmd = &ctx->pending_cmd;

        switch (cmd->type) {
        case CMD_TYPE_HELLO:
            /* HELLO is the only command that establishes brain connection. */
            ctx->link_ticks           = 0;
            ctx->brain_ever_connected = true;
            if (ctx->state == SM_STATE_FAULT_LINK_LOST) {
                ctx->state = SM_STATE_IDLE;
            }
            break;

        case CMD_TYPE_HEARTBEAT:
            /* Resets the link watchdog; connection must already be up. */
            if (ctx->brain_ever_connected) { ctx->link_ticks = 0; }
            break;

        case CMD_TYPE_EMERGENCY_STOP:
            /* Always accepted regardless of connection state. */
            if (ctx->brain_ever_connected) { ctx->link_ticks = 0; }
            ctx->az_cmd       = SM_AZ_STOP;
            ctx->el_cmd       = SM_EL_STOP;
            ctx->estop_active = true;
            /* Hardware ESTOP (big red button) latches and BLOCKS all subsequent
               motion commands until the operator sends CLEAR_FAULT.  Software
               ESTOP (from the brain) sets estop_active but does not latch —
               the next SET_MOTION clears it as before. */
            if (cmd->source == CMD_SRC_LOCAL) {
                ctx->estop_hw_latch = true;
            }
            if (!is_faulted(ctx)) { ctx->state = SM_STATE_IDLE; }
            break;

        case CMD_TYPE_SET_MOTION:
            if (!ctx->brain_ever_connected) { break; }
            /* Hardware ESTOP latch blocks motion until CLEAR_FAULT is received. */
            if (ctx->estop_hw_latch) { break; }
            ctx->link_ticks   = 0;
            ctx->estop_active = false;
            if (!is_faulted(ctx)) {
                if (ctx->state == SM_STATE_PARKING) {
                    ctx->state = SM_STATE_IDLE;
                }
                ctx->az_cmd = (sm_az_motion_t)cmd->motion.az;
                ctx->el_cmd = (sm_el_motion_t)cmd->motion.el;
            }
            break;

        case CMD_TYPE_PARK:
            /* Accepted from any source, even without prior HELLO.
               Link-loss is suppressed while in SM_STATE_PARKING so the
               antenna finishes parking even if the brain disconnects. */
            ctx->link_ticks           = 0;
            ctx->brain_ever_connected = true;
            if (!is_faulted(ctx)) {
                ctx->state  = SM_STATE_PARKING;
                ctx->az_cmd = SM_AZ_STOP;   /* parking controller takes over */
                ctx->el_cmd = SM_EL_STOP;
            }
            break;

        case CMD_TYPE_SET_POLARIZATION:
            if (!ctx->brain_ever_connected) { break; }
            ctx->link_ticks  = 0;
            ctx->pol_vhf  = cmd->pol.pol_vhf;
            ctx->pol_uhf  = cmd->pol.pol_uhf;
            ctx->lna_uhf  = cmd->pol.lna_uhf;
            ctx->rxtx_uhf = cmd->pol.rxtx_uhf;
            break;

        case CMD_TYPE_SET_LIMITS:
            if (!ctx->brain_ever_connected) { break; }
            ctx->link_ticks = 0;
            ctx->az_min = cmd->limits.az_min;
            ctx->az_max = cmd->limits.az_max;
            ctx->el_min = cmd->limits.el_min;
            ctx->el_max = cmd->limits.el_max;
            break;

        case CMD_TYPE_CLEAR_FAULT:
            if (!ctx->brain_ever_connected) { break; }
            ctx->link_ticks     = 0;
            ctx->estop_active   = false;
            ctx->estop_hw_latch = false;   /* operator acknowledges — motion resumes */
            /* Duty-cycle fault: clear fault flag but preserve counters.
               If rest is insufficient, motion commands will re-fault. */
            if (is_faulted(ctx)) {
                ctx->state  = SM_STATE_IDLE;
                ctx->az_cmd = SM_AZ_STOP;
                ctx->el_cmd = SM_EL_STOP;
            }
            break;

        case CMD_TYPE_SET_NETCONFIG:
        case CMD_TYPE_RESET_NETCONFIG:
        case CMD_TYPE_SET_BLOCK:
        case CMD_TYPE_SET_BLOCKS:
        case CMD_TYPE_RESET_BLOCKS:
        case CMD_TYPE_REBOOT:
            break;  /* handled in net.c before reaching the state machine */
        }

        ctx->has_command = false;
    }

    /* ── 2. link-loss watchdog (suppressed during parking) ─────────────── */
    if (ctx->brain_ever_connected && ctx->state != SM_STATE_PARKING) {
        ctx->link_ticks++;
        if (ctx->link_ticks >= LINK_TIMEOUT_TICKS && !is_faulted(ctx)) {
            enter_fault(ctx, SM_STATE_FAULT_LINK_LOST);
        }
    }

    /* ── 2.5. Parking position controller ──────────────────────────────── */
    if (ctx->state == SM_STATE_PARKING && !is_faulted(ctx)) {
        float az_err = PARK_AZ_NORM - in->az_pos;
        float el_err = PARK_EL_NORM - in->el_pos;
        float az_abs = (az_err < 0.0f) ? -az_err : az_err;
        float el_abs = (el_err < 0.0f) ? -el_err : el_err;

        ctx->az_cmd = (az_abs <= PARK_TOLERANCE) ? SM_AZ_STOP :
                      (az_err > 0.0f) ? SM_AZ_CW : SM_AZ_CCW;
        ctx->el_cmd = (el_abs <= PARK_TOLERANCE) ? SM_EL_STOP :
                      (el_err > 0.0f) ? SM_EL_UP : SM_EL_DOWN;

        if (ctx->az_cmd == SM_AZ_STOP && ctx->el_cmd == SM_EL_STOP) {
            ctx->state = SM_STATE_IDLE;   /* parking complete */
        }
    }

    /* ── 3. ADC validity check (latching) ───────────────────────────────── */
    if (!in->adc_valid && !is_faulted(ctx)) {
        enter_fault(ctx, SM_STATE_FAULT_ADC_INVALID);
    }

    /* ── 4. If faulted: motors off, hold RF switches, done ─────────────── */
    if (is_faulted(ctx)) {
        return (sm_output_t){
            .az_dir   = SM_AZ_STOP,
            .el_dir   = SM_EL_STOP,
            .pol_vhf  = ctx->pol_vhf,
            .pol_uhf  = ctx->pol_uhf,
            .lna_uhf  = ctx->lna_uhf,
            .rxtx_uhf = ctx->rxtx_uhf,
        };
    }

    /* ── 5. Duty-cycle check before allowing motion ─────────────────────── */
    if (ctx->az_cmd != SM_AZ_STOP) {
        if (ctx->az_on_ticks >= DUTY_MAX_ON_TICKS) {
            enter_fault(ctx, SM_STATE_FAULT_DUTY_CYCLE);
            goto build_output;
        }
    }
    if (ctx->el_cmd != SM_EL_STOP) {
        if (ctx->el_on_ticks >= DUTY_MAX_ON_TICKS) {
            enter_fault(ctx, SM_STATE_FAULT_DUTY_CYCLE);
            goto build_output;
        }
    }

    /* ── 6. Soft-limit check ────────────────────────────────────────────── */
    if (ctx->az_cmd == SM_AZ_CW  && in->az_pos >= ctx->az_max) {
        ctx->az_cmd = SM_AZ_STOP;
        enter_fault(ctx, SM_STATE_FAULT_LIMIT);
        goto build_output;
    }
    if (ctx->az_cmd == SM_AZ_CCW && in->az_pos <= ctx->az_min) {
        ctx->az_cmd = SM_AZ_STOP;
        enter_fault(ctx, SM_STATE_FAULT_LIMIT);
        goto build_output;
    }
    if (ctx->el_cmd == SM_EL_UP   && in->el_pos >= ctx->el_max) {
        ctx->el_cmd = SM_EL_STOP;
        enter_fault(ctx, SM_STATE_FAULT_LIMIT);
        goto build_output;
    }
    if (ctx->el_cmd == SM_EL_DOWN && in->el_pos <= ctx->el_min) {
        ctx->el_cmd = SM_EL_STOP;
        enter_fault(ctx, SM_STATE_FAULT_LIMIT);
        goto build_output;
    }

    /* ── 6b. AZ block el_floor enforcement ───────────────────────────────
     * If the current AZ sector has a minimum elevation floor and the antenna
     * is at or below it, prevent further EL DOWN movement.  No fault — it is
     * expected behaviour at an obstacle boundary. */
    if (ctx->el_cmd == SM_EL_DOWN && in->el_pos <= in->el_floor) {
        ctx->el_cmd = SM_EL_STOP;
    }

    /* ── 7. Update duty-cycle counters ──────────────────────────────────── */
    if (ctx->az_cmd != SM_AZ_STOP) {
        ctx->az_on_ticks++;
        ctx->az_rest_ticks = 0;
    } else {
        if (ctx->az_rest_ticks < DUTY_MIN_REST_TICKS) {
            ctx->az_rest_ticks++;
        } else {
            ctx->az_on_ticks = 0;   /* rest complete — on-time budget resets */
        }
    }
    if (ctx->el_cmd != SM_EL_STOP) {
        ctx->el_on_ticks++;
        ctx->el_rest_ticks = 0;
    } else {
        if (ctx->el_rest_ticks < DUTY_MIN_REST_TICKS) {
            ctx->el_rest_ticks++;
        } else {
            ctx->el_on_ticks = 0;
        }
    }

    /* ── 8. Update top-level state ──────────────────────────────────────── */
    if (ctx->state != SM_STATE_PARKING) {   /* parking manages its own state */
        ctx->state = (ctx->az_cmd != SM_AZ_STOP || ctx->el_cmd != SM_EL_STOP)
                     ? SM_STATE_MOVING : SM_STATE_IDLE;
    }

build_output:
    return (sm_output_t){
        .az_dir   = is_faulted(ctx) ? SM_AZ_STOP : ctx->az_cmd,
        .el_dir   = is_faulted(ctx) ? SM_EL_STOP : ctx->el_cmd,
        .pol_vhf  = ctx->pol_vhf,
        .pol_uhf  = ctx->pol_uhf,
        .lna_uhf  = ctx->lna_uhf,
        .rxtx_uhf = ctx->rxtx_uhf,
    };
}

/* ── telemetry helpers ────────────────────────────────────────────────────── */

sm_state_t     sm_get_state(const sm_ctx_t *ctx)     { return ctx->state; }
sm_az_motion_t sm_get_az_motion(const sm_ctx_t *ctx) { return ctx->az_cmd; }
sm_el_motion_t sm_get_el_motion(const sm_ctx_t *ctx) { return ctx->el_cmd; }

uint8_t sm_duty_az_pct(const sm_ctx_t *ctx)
{
    if (ctx->az_on_ticks >= DUTY_MAX_ON_TICKS) { return 100; }
    return (uint8_t)((ctx->az_on_ticks * 100UL) / DUTY_MAX_ON_TICKS);
}

uint8_t sm_duty_el_pct(const sm_ctx_t *ctx)
{
    if (ctx->el_on_ticks >= DUTY_MAX_ON_TICKS) { return 100; }
    return (uint8_t)((ctx->el_on_ticks * 100UL) / DUTY_MAX_ON_TICKS);
}

const char *sm_state_str(sm_state_t s)
{
    switch (s) {
    case SM_STATE_IDLE:              return "IDLE";
    case SM_STATE_MOVING:            return "MOVING";
    case SM_STATE_PARKING:           return "PARKING";
    case SM_STATE_FAULT_LINK_LOST:   return "FAULT_LINK_LOST";
    case SM_STATE_FAULT_LIMIT:       return "FAULT_LIMIT";
    case SM_STATE_FAULT_DUTY_CYCLE:  return "FAULT_DUTY_CYCLE";
    case SM_STATE_FAULT_ADC_INVALID: return "FAULT_ADC_INVALID";
    default:                         return "UNKNOWN";
    }
}

const char *sm_az_str(sm_az_motion_t m)
{
    switch (m) {
    case SM_AZ_CW:  return "cw";
    case SM_AZ_CCW: return "ccw";
    default:        return "stop";
    }
}

const char *sm_el_str(sm_el_motion_t m)
{
    switch (m) {
    case SM_EL_UP:   return "up";
    case SM_EL_DOWN: return "down";
    default:         return "stop";
    }
}
