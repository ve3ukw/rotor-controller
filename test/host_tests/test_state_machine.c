#include <assert.h>
#include <stdio.h>
#include <string.h>
#include <stdint.h>
#include <stdbool.h>

#include "state_machine.h"
#include "config.h"

/* ── helpers ──────────────────────────────────────────────────────────────── */

#define PASS(name) printf("  PASS  %s\n", name)

static sm_input_t good_input(void)
{
    return (sm_input_t){ .az_pos = 0.5f, .el_pos = 0.5f, .adc_valid = true };
}

/* Push a HELLO command to establish brain connection. */
static void connect_brain(sm_ctx_t *ctx)
{
    sm_command_t cmd = { .type = CMD_TYPE_HELLO,
                         .source = CMD_SRC_TCP, .priority = 1 };
    sm_push_command(ctx, &cmd);
    sm_input_t in = good_input();
    sm_tick(ctx, &in);
}

/* Advance N ticks with good input and no commands. */
static sm_output_t run_ticks(sm_ctx_t *ctx, uint32_t n)
{
    sm_input_t in = good_input();
    sm_output_t out = {0};
    for (uint32_t i = 0; i < n; i++) { out = sm_tick(ctx, &in); }
    return out;
}

/* Advance N ticks, injecting heartbeats to prevent link-loss.
   Heartbeats start at i=1 so a pending motion command at i=0 is not displaced.
   Interval is well inside the 2-second link-loss window. */
#define HB_INTERVAL  (TICK_HZ)   /* 1 s — half the link-loss timeout */
static sm_output_t run_ticks_with_hb(sm_ctx_t *ctx, uint32_t n)
{
    sm_input_t in = good_input();
    sm_output_t out = {0};
    sm_command_t hb = { .type = CMD_TYPE_HEARTBEAT,
                        .source = CMD_SRC_TCP, .priority = 1 };
    for (uint32_t i = 0; i < n; i++) {
        if (i > 0 && (i % HB_INTERVAL) == 0) { sm_push_command(ctx, &hb); }
        out = sm_tick(ctx, &in);
    }
    return out;
}

static void push_motion(sm_ctx_t *ctx, uint8_t az, uint8_t el)
{
    sm_command_t cmd = {
        .type = CMD_TYPE_SET_MOTION, .source = CMD_SRC_TCP, .priority = 1,
        .motion = { .az = az, .el = el },
    };
    sm_push_command(ctx, &cmd);
}

/* ── test cases ───────────────────────────────────────────────────────────── */

static void test_boot_safe_state(void)
{
    sm_ctx_t ctx; sm_init(&ctx);
    assert(ctx.state == SM_STATE_IDLE);
    assert(ctx.az_cmd == SM_AZ_STOP);
    assert(ctx.el_cmd == SM_EL_STOP);
    PASS("boot_safe_state");
}

static void test_no_motion_before_hello(void)
{
    sm_ctx_t ctx; sm_init(&ctx);
    push_motion(&ctx, SM_AZ_CW, SM_EL_STOP);
    sm_input_t in = good_input();
    sm_output_t out = sm_tick(&ctx, &in);
    /* Brain not connected — motion must be rejected. */
    assert(out.az_dir == SM_AZ_STOP);
    assert(ctx.state  == SM_STATE_IDLE);
    PASS("no_motion_before_hello");
}

static void test_idle_to_moving(void)
{
    sm_ctx_t ctx; sm_init(&ctx);
    connect_brain(&ctx);
    push_motion(&ctx, SM_AZ_CW, SM_EL_STOP);
    sm_output_t out = run_ticks(&ctx, 1);
    assert(out.az_dir  == SM_AZ_CW);
    assert(out.el_dir  == SM_EL_STOP);
    assert(ctx.state   == SM_STATE_MOVING);
    PASS("idle_to_moving");
}

static void test_az_el_independent(void)
{
    sm_ctx_t ctx; sm_init(&ctx);
    connect_brain(&ctx);
    push_motion(&ctx, SM_AZ_CCW, SM_EL_UP);
    sm_output_t out = run_ticks(&ctx, 1);
    assert(out.az_dir == SM_AZ_CCW);
    assert(out.el_dir == SM_EL_UP);
    assert(ctx.state  == SM_STATE_MOVING);
    PASS("az_el_independent");
}

static void test_stop_returns_to_idle(void)
{
    sm_ctx_t ctx; sm_init(&ctx);
    connect_brain(&ctx);
    push_motion(&ctx, SM_AZ_CW, SM_EL_STOP);
    run_ticks(&ctx, 1);
    push_motion(&ctx, SM_AZ_STOP, SM_EL_STOP);
    sm_output_t out = run_ticks(&ctx, 1);
    assert(out.az_dir == SM_AZ_STOP);
    assert(ctx.state  == SM_STATE_IDLE);
    PASS("stop_returns_to_idle");
}

static void test_emergency_stop(void)
{
    sm_ctx_t ctx; sm_init(&ctx);
    connect_brain(&ctx);
    push_motion(&ctx, SM_AZ_CW, SM_EL_UP);
    run_ticks(&ctx, 1);
    assert(ctx.state == SM_STATE_MOVING);

    sm_command_t estop = { .type = CMD_TYPE_EMERGENCY_STOP,
                           .source = CMD_SRC_TCP, .priority = 255 };
    sm_push_command(&ctx, &estop);
    sm_output_t out = run_ticks(&ctx, 1);
    assert(out.az_dir == SM_AZ_STOP);
    assert(out.el_dir == SM_EL_STOP);
    assert(ctx.state  == SM_STATE_IDLE);
    PASS("emergency_stop");
}

static void test_link_lost_fault(void)
{
    sm_ctx_t ctx; sm_init(&ctx);
    connect_brain(&ctx);
    /* Run LINK_TIMEOUT_TICKS + 1 without any command. */
    run_ticks(&ctx, TICK_HZ * 2 + 1);
    assert(ctx.state == SM_STATE_FAULT_LINK_LOST);
    PASS("link_lost_fault");
}

static void test_heartbeat_resets_watchdog(void)
{
    sm_ctx_t ctx; sm_init(&ctx);
    connect_brain(&ctx);
    /* After connect_brain, link_ticks = 1 (watchdog ticks in same tick as HELLO).
       Run to 2 ticks before fault (198 ticks → link_ticks = 199, still safe). */
    run_ticks(&ctx, TICK_HZ * 2 - 2);
    assert(ctx.state != SM_STATE_FAULT_LINK_LOST);
    /* Send a heartbeat — resets the counter. */
    sm_command_t hb = { .type = CMD_TYPE_HEARTBEAT,
                        .source = CMD_SRC_TCP, .priority = 1 };
    sm_push_command(&ctx, &hb);
    run_ticks(&ctx, 1);   /* heartbeat processed → link_ticks = 0, then +1 = 1 */
    /* Run to 2 ticks before fault again — still no fault. */
    run_ticks(&ctx, TICK_HZ * 2 - 2);
    assert(ctx.state != SM_STATE_FAULT_LINK_LOST);
    PASS("heartbeat_resets_watchdog");
}

static void test_link_lost_clears_on_hello(void)
{
    sm_ctx_t ctx; sm_init(&ctx);
    connect_brain(&ctx);
    run_ticks(&ctx, TICK_HZ * 2 + 1);
    assert(ctx.state == SM_STATE_FAULT_LINK_LOST);
    connect_brain(&ctx);   /* reconnect */
    assert(ctx.state == SM_STATE_IDLE);
    PASS("link_lost_clears_on_hello");
}

static void test_adc_invalid_fault(void)
{
    sm_ctx_t ctx; sm_init(&ctx);
    connect_brain(&ctx);
    sm_input_t bad = { .az_pos = 0.5f, .el_pos = 0.5f, .adc_valid = false };
    sm_tick(&ctx, &bad);
    assert(ctx.state == SM_STATE_FAULT_ADC_INVALID);
    PASS("adc_invalid_fault");
}

static void test_clear_fault(void)
{
    sm_ctx_t ctx; sm_init(&ctx);
    connect_brain(&ctx);
    /* Trigger ADC fault. */
    sm_input_t bad = { .adc_valid = false };
    sm_tick(&ctx, &bad);
    assert(ctx.state == SM_STATE_FAULT_ADC_INVALID);
    /* Clear it. */
    sm_command_t clr = { .type = CMD_TYPE_CLEAR_FAULT,
                         .source = CMD_SRC_TCP, .priority = 1 };
    sm_push_command(&ctx, &clr);
    sm_input_t good = good_input();
    sm_tick(&ctx, &good);
    assert(ctx.state == SM_STATE_IDLE);
    PASS("clear_fault");
}

static void test_soft_limit_az_max(void)
{
    sm_ctx_t ctx; sm_init(&ctx);
    connect_brain(&ctx);
    /* Set az limit to 0.8. */
    sm_command_t lim = {
        .type = CMD_TYPE_SET_LIMITS, .source = CMD_SRC_TCP, .priority = 1,
        .limits = { .az_min = 0.0f, .az_max = 0.8f,
                    .el_min = 0.0f, .el_max = 1.0f },
    };
    sm_push_command(&ctx, &lim);
    run_ticks(&ctx, 1);

    /* Command CW while az is already at the limit. */
    push_motion(&ctx, SM_AZ_CW, SM_EL_STOP);
    sm_input_t at_limit = { .az_pos = 0.8f, .el_pos = 0.5f, .adc_valid = true };
    sm_push_command(&ctx, &(sm_command_t){
        .type = CMD_TYPE_SET_MOTION, .source = CMD_SRC_TCP, .priority = 1,
        .motion = { .az = SM_AZ_CW, .el = SM_EL_STOP },
    });
    sm_tick(&ctx, &at_limit);
    assert(ctx.state == SM_STATE_FAULT_LIMIT);
    PASS("soft_limit_az_max");
}

static void test_duty_cycle_fault(void)
{
    sm_ctx_t ctx; sm_init(&ctx);
    connect_brain(&ctx);
    push_motion(&ctx, SM_AZ_CW, SM_EL_STOP);

    /* Run az motor for exactly MAX_ON ticks — fault fires on the next.
       Inject heartbeats so link-loss doesn't fire first. */
    uint32_t max_on = TICK_HZ * 60U * 3U;
    run_ticks_with_hb(&ctx, max_on);
    assert(ctx.state != SM_STATE_FAULT_DUTY_CYCLE);  /* not yet */
    sm_input_t in = good_input();
    sm_tick(&ctx, &in);                              /* one more tick */
    assert(ctx.state == SM_STATE_FAULT_DUTY_CYCLE);
    PASS("duty_cycle_fault");
}

static void test_priority_higher_wins(void)
{
    sm_ctx_t ctx; sm_init(&ctx);
    connect_brain(&ctx);

    sm_command_t low = {
        .type = CMD_TYPE_SET_MOTION, .source = CMD_SRC_TCP, .priority = 0,
        .motion = { .az = SM_AZ_CW, .el = SM_EL_STOP },
    };
    sm_command_t high = {
        .type = CMD_TYPE_SET_MOTION, .source = CMD_SRC_TCP, .priority = 2,
        .motion = { .az = SM_AZ_CCW, .el = SM_EL_UP },
    };
    sm_push_command(&ctx, &low);
    sm_push_command(&ctx, &high);   /* higher priority — should win */
    sm_output_t out = run_ticks(&ctx, 1);
    assert(out.az_dir == SM_AZ_CCW);
    assert(out.el_dir == SM_EL_UP);
    PASS("priority_higher_wins");
}

static void test_polarization_set(void)
{
    sm_ctx_t ctx; sm_init(&ctx);
    connect_brain(&ctx);
    sm_command_t cmd = {
        .type = CMD_TYPE_SET_POLARIZATION, .source = CMD_SRC_TCP, .priority = 1,
        .pol = { .pol_vhf = true, .pol_uhf = false,
                 .lna_uhf = true, .rxtx_uhf = false },
    };
    sm_push_command(&ctx, &cmd);
    sm_output_t out = run_ticks(&ctx, 1);
    assert(out.pol_vhf == true);
    assert(out.pol_uhf == false);
    assert(out.lna_uhf == true);
    assert(out.rxtx_uhf == false);
    PASS("polarization_set");
}

/* ── main ─────────────────────────────────────────────────────────────────── */

int main(void)
{
    printf("state machine tests\n");

    test_boot_safe_state();
    test_no_motion_before_hello();
    test_idle_to_moving();
    test_az_el_independent();
    test_stop_returns_to_idle();
    test_emergency_stop();
    test_link_lost_fault();
    test_heartbeat_resets_watchdog();
    test_link_lost_clears_on_hello();
    test_adc_invalid_fault();
    test_clear_fault();
    test_soft_limit_az_max();
    test_duty_cycle_fault();
    test_priority_higher_wins();
    test_polarization_set();

    printf("all tests passed\n");
    return 0;
}
