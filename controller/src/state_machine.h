#pragma once
#include <stdint.h>
#include <stdbool.h>
#include "command.h"

/* ── motion and state types ───────────────────────────────────────────────── */

typedef enum {
    SM_AZ_STOP = 0,
    SM_AZ_CW,
    SM_AZ_CCW,
} sm_az_motion_t;

typedef enum {
    SM_EL_STOP = 0,
    SM_EL_UP,
    SM_EL_DOWN,
} sm_el_motion_t;

typedef enum {
    SM_STATE_IDLE = 0,
    SM_STATE_MOVING,
    SM_STATE_PARKING,           /* autonomously moving to park position */
    SM_STATE_FAULT_LINK_LOST,
    SM_STATE_FAULT_DUTY_CYCLE,
    SM_STATE_FAULT_ADC_INVALID,
} sm_state_t;

/* ── inputs (from sensors and ADC module each tick) ──────────────────────── */

typedef struct {
    float az_pos;      /* normalized 0..1 */
    float el_pos;
    bool  adc_valid;
    float el_floor;    /* min normalized elevation for current AZ (from blocks) */
} sm_input_t;

/* ── outputs (caller applies to GPIO each tick) ──────────────────────────── */

typedef struct {
    sm_az_motion_t az_dir;
    sm_el_motion_t el_dir;
    bool pol_vhf;
    bool pol_uhf;
    bool lna_uhf;
    bool rxtx_uhf;
} sm_output_t;

/* ── mutable context (zero-init at boot = safe defaults) ─────────────────── */

typedef struct {
    sm_state_t     state;
    sm_az_motion_t az_cmd;      /* latest brain-commanded az direction */
    sm_el_motion_t el_cmd;

    /* Soft limits — permissive defaults, overwritten by set_limits command */
    float az_min, az_max;
    float el_min, el_max;

    /* Park position — defaults from config.h, overwritten by set_park command */
    float park_az, park_el;

    /* Link-loss watchdog */
    uint32_t link_ticks;
    bool     brain_ever_connected;

    /* estop_active: set by software ESTOP (brain command); cleared by SET_MOTION or
       CLEAR_FAULT.  Used for display feedback on software-initiated stops. */
    bool     estop_active;

    /* estop_hw_latch: set when hardware A9 ESTOP is triggered (CMD_SRC_LOCAL).
       Unlike estop_active, this BLOCKS all SET_MOTION commands until the operator
       explicitly sends CLEAR_FAULT.  Releasing the A9 button alone does not clear
       it — the operator must actively acknowledge before motion resumes. */
    bool     estop_hw_latch;

    /* Duty-cycle counters (per axis, in ticks) */
    uint32_t az_on_ticks;
    uint32_t az_rest_ticks;
    uint32_t el_on_ticks;
    uint32_t el_rest_ticks;

    /* RF / antenna switch states */
    bool pol_vhf;
    bool pol_uhf;
    bool lna_uhf;
    bool rxtx_uhf;

    /* Single-slot command queue (highest-priority pending command) */
    bool         has_command;
    sm_command_t pending_cmd;
} sm_ctx_t;

/* ── API ──────────────────────────────────────────────────────────────────── */

/* Set context to safe boot defaults. */
void sm_init(sm_ctx_t *ctx);

/* Push a command from any source.  Call from main-loop context only.
   Emergency-stop always wins; otherwise higher priority displaces lower. */
void sm_push_command(sm_ctx_t *ctx, const sm_command_t *cmd);

/* Advance one tick.  Updates ctx in place; returns desired GPIO outputs. */
sm_output_t sm_tick(sm_ctx_t *ctx, const sm_input_t *in);

/* Telemetry helpers. */
sm_state_t     sm_get_state(const sm_ctx_t *ctx);
sm_az_motion_t sm_get_az_motion(const sm_ctx_t *ctx);
sm_el_motion_t sm_get_el_motion(const sm_ctx_t *ctx);
uint8_t        sm_duty_az_pct(const sm_ctx_t *ctx);
uint8_t        sm_duty_el_pct(const sm_ctx_t *ctx);
const char    *sm_state_str(sm_state_t s);
const char    *sm_az_str(sm_az_motion_t m);
const char    *sm_el_str(sm_el_motion_t m);
