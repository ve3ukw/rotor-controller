#include <assert.h>
#include <stdio.h>
#include <string.h>
#include <stdint.h>
#include <stdbool.h>

#include "protocol.h"

#define PASS(name) printf("  PASS  %s\n", name)

/* ── parser tests ─────────────────────────────────────────────────────────── */

static void test_parse_hello(void)
{
    sm_command_t cmd; uint32_t seq = 0;
    assert(protocol_parse("{\"type\":\"hello\",\"seq\":1,\"client\":\"brain-v1\"}", &seq, &cmd));
    assert(seq == 1);
    assert(cmd.type == CMD_TYPE_HELLO);
    PASS("parse_hello");
}

static void test_parse_heartbeat(void)
{
    sm_command_t cmd; uint32_t seq = 0;
    assert(protocol_parse("{\"type\":\"heartbeat\",\"seq\":42}", &seq, &cmd));
    assert(seq == 42);
    assert(cmd.type == CMD_TYPE_HEARTBEAT);
    PASS("parse_heartbeat");
}

static void test_parse_set_motion(void)
{
    sm_command_t cmd; uint32_t seq = 0;
    assert(protocol_parse(
        "{\"type\":\"set_motion\",\"seq\":7,\"az\":\"cw\",\"el\":\"stop\"}", &seq, &cmd));
    assert(seq == 7);
    assert(cmd.type == CMD_TYPE_SET_MOTION);
    assert(cmd.motion.az == SM_AZ_CW);
    assert(cmd.motion.el == SM_EL_STOP);
    PASS("parse_set_motion");
}

static void test_parse_set_motion_all_dirs(void)
{
    sm_command_t cmd; uint32_t seq;
    assert(protocol_parse(
        "{\"type\":\"set_motion\",\"seq\":1,\"az\":\"ccw\",\"el\":\"up\"}", &seq, &cmd));
    assert(cmd.motion.az == SM_AZ_CCW);
    assert(cmd.motion.el == SM_EL_UP);

    assert(protocol_parse(
        "{\"type\":\"set_motion\",\"seq\":2,\"az\":\"stop\",\"el\":\"down\"}", &seq, &cmd));
    assert(cmd.motion.az == SM_AZ_STOP);
    assert(cmd.motion.el == SM_EL_DOWN);
    PASS("parse_set_motion_all_dirs");
}

static void test_parse_set_polarization(void)
{
    sm_command_t cmd; uint32_t seq = 0;
    assert(protocol_parse(
        "{\"type\":\"set_polarization\",\"seq\":3,"
        "\"pol_vhf\":true,\"pol_uhf\":false,"
        "\"lna_uhf\":true,\"rxtx_uhf\":false}", &seq, &cmd));
    assert(cmd.type == CMD_TYPE_SET_POLARIZATION);
    assert(cmd.pol.pol_vhf  == true);
    assert(cmd.pol.pol_uhf  == false);
    assert(cmd.pol.lna_uhf  == true);
    assert(cmd.pol.rxtx_uhf == false);
    PASS("parse_set_polarization");
}

static void test_parse_set_limits(void)
{
    sm_command_t cmd; uint32_t seq = 0;
    assert(protocol_parse(
        "{\"type\":\"set_limits\",\"seq\":5,"
        "\"az_min\":0.05,\"az_max\":0.95,"
        "\"el_min\":0.0,\"el_max\":1.0}", &seq, &cmd));
    assert(cmd.type == CMD_TYPE_SET_LIMITS);
    assert(cmd.limits.az_min > 0.04f && cmd.limits.az_min < 0.06f);
    assert(cmd.limits.az_max > 0.94f && cmd.limits.az_max < 0.96f);
    assert(cmd.limits.el_min == 0.0f);
    assert(cmd.limits.el_max == 1.0f);
    PASS("parse_set_limits");
}

static void test_parse_clear_fault(void)
{
    sm_command_t cmd; uint32_t seq = 0;
    assert(protocol_parse("{\"type\":\"clear_fault\",\"seq\":99}", &seq, &cmd));
    assert(seq == 99);
    assert(cmd.type == CMD_TYPE_CLEAR_FAULT);
    PASS("parse_clear_fault");
}

static void test_parse_emergency_stop(void)
{
    sm_command_t cmd; uint32_t seq = 0;
    assert(protocol_parse("{\"type\":\"emergency_stop\",\"seq\":0}", &seq, &cmd));
    assert(cmd.type == CMD_TYPE_EMERGENCY_STOP);
    assert(cmd.priority == 255);
    PASS("parse_emergency_stop");
}

static void test_parse_unknown_type(void)
{
    sm_command_t cmd; uint32_t seq = 0;
    assert(!protocol_parse("{\"type\":\"reboot\",\"seq\":1}", &seq, &cmd));
    PASS("parse_unknown_type");
}

static void test_parse_missing_seq(void)
{
    sm_command_t cmd; uint32_t seq = 0;
    assert(!protocol_parse("{\"type\":\"heartbeat\"}", &seq, &cmd));
    PASS("parse_missing_seq");
}

static void test_parse_empty(void)
{
    sm_command_t cmd; uint32_t seq = 0;
    assert(!protocol_parse("", &seq, &cmd));
    assert(!protocol_parse("{}", &seq, &cmd));
    PASS("parse_empty");
}

/* ── encoder tests ────────────────────────────────────────────────────────── */

static void test_encode_ack_ok(void)
{
    const char *s = protocol_encode_ack(7, true, NULL);
    assert(strstr(s, "\"type\":\"ack\""));
    assert(strstr(s, "\"seq\":7"));
    assert(strstr(s, "\"ok\":true"));
    assert(s[strlen(s) - 1] == '\n');
    PASS("encode_ack_ok");
}

static void test_encode_ack_error(void)
{
    const char *s = protocol_encode_ack(3, false, "parse error");
    assert(strstr(s, "\"ok\":false"));
    assert(strstr(s, "parse error"));
    PASS("encode_ack_error");
}

static void test_encode_telemetry_fields(void)
{
    sm_ctx_t sm; sm_init(&sm);
    const char *s = protocol_encode_telemetry(&sm, 0.5f, 0.25f, 1000, 5);
    assert(strstr(s, "\"type\":\"telemetry\""));
    assert(strstr(s, "\"seq\":5"));
    assert(strstr(s, "\"ts_ms\":1000"));
    assert(strstr(s, "\"az_raw\":0.5"));
    assert(strstr(s, "\"el_raw\":0.25"));
    assert(strstr(s, "\"state\":\"IDLE\""));
    assert(s[strlen(s) - 1] == '\n');
    PASS("encode_telemetry_fields");
}

/* ── main ─────────────────────────────────────────────────────────────────── */

int main(void)
{
    printf("protocol tests\n");

    test_parse_hello();
    test_parse_heartbeat();
    test_parse_set_motion();
    test_parse_set_motion_all_dirs();
    test_parse_set_polarization();
    test_parse_set_limits();
    test_parse_clear_fault();
    test_parse_emergency_stop();
    test_parse_unknown_type();
    test_parse_missing_seq();
    test_parse_empty();
    test_encode_ack_ok();
    test_encode_ack_error();
    test_encode_telemetry_fields();

    printf("all tests passed\n");
    return 0;
}
