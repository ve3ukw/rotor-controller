#include <stdio.h>    /* snprintf, sscanf */
#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>
#include <string.h>
#include <stdlib.h>   /* strtoul, strtof */

#include "protocol.h"

/* ── static output buffers ────────────────────────────────────────────────── */
static char g_ack_buf[128];
static char g_telem_buf[640];

/* ── JSON field extractors ────────────────────────────────────────────────── */

/* Returns pointer to the value part of "key":VALUE in json, or NULL. */
static const char *find_val(const char *json, const char *key)
{
    /* Build search token  "key":  */
    char needle[48];
    size_t klen = strlen(key);
    if (klen + 4 >= sizeof(needle)) { return NULL; }
    needle[0] = '"';
    memcpy(needle + 1, key, klen);
    needle[klen + 1] = '"';
    needle[klen + 2] = ':';
    needle[klen + 3] = '\0';

    const char *p = strstr(json, needle);
    if (!p) { return NULL; }
    p += klen + 3;
    while (*p == ' ') { p++; }   /* skip optional whitespace */
    return p;
}

static bool get_string(const char *json, const char *key,
                        char *out, size_t maxlen)
{
    const char *p = find_val(json, key);
    if (!p || *p != '"') { return false; }
    p++;
    size_t i = 0;
    while (*p && *p != '"' && i < maxlen - 1) { out[i++] = *p++; }
    if (*p != '"') { return false; }
    out[i] = '\0';
    return true;
}

static bool get_uint32(const char *json, const char *key, uint32_t *out)
{
    const char *p = find_val(json, key);
    if (!p) { return false; }
    char *end;
    unsigned long v = strtoul(p, &end, 10);
    if (end == p) { return false; }
    *out = (uint32_t)v;
    return true;
}

static bool get_float(const char *json, const char *key, float *out)
{
    const char *p = find_val(json, key);
    if (!p) { return false; }
    char *end;
    float v = strtof(p, &end);
    if (end == p) { return false; }
    *out = v;
    return true;
}

static bool get_bool(const char *json, const char *key, bool *out)
{
    const char *p = find_val(json, key);
    if (!p) { return false; }
    if (strncmp(p, "true",  4) == 0) { *out = true;  return true; }
    if (strncmp(p, "false", 5) == 0) { *out = false; return true; }
    return false;
}

/* ── float → "X.YYYY" without floating-point printf ──────────────────────── */
/* Only correct for 0.0 ≤ v ≤ 1.0 — sufficient for normalized ADC readings. */
static int fmt_01(char *buf, size_t n, float v)
{
    if (v < 0.0f) { v = 0.0f; }
    if (v > 1.0f) { v = 1.0f; }
    unsigned frac = (unsigned)(v * 10000.0f + 0.5f);
    /* frac may be 10000 due to rounding — clamp */
    if (frac >= 10000u) { frac = 9999u; }
    return snprintf(buf, n, "0.%04u", frac);
}

/* ── public API ───────────────────────────────────────────────────────────── */

bool protocol_parse(const char *json, uint32_t *seq, sm_command_t *cmd)
{
    char type[24] = {0};
    if (!get_string(json, "type", type, sizeof(type))) { return false; }
    if (!get_uint32(json, "seq", seq)) { return false; }

    memset(cmd, 0, sizeof(*cmd));
    cmd->source   = CMD_SRC_TCP;
    cmd->priority = 1;

    if (strcmp(type, "hello") == 0) {
        cmd->type = CMD_TYPE_HELLO;
        return true;
    }
    if (strcmp(type, "heartbeat") == 0) {
        cmd->type = CMD_TYPE_HEARTBEAT;
        return true;
    }
    if (strcmp(type, "clear_fault") == 0) {
        cmd->type = CMD_TYPE_CLEAR_FAULT;
        return true;
    }
    if (strcmp(type, "emergency_stop") == 0) {
        cmd->type     = CMD_TYPE_EMERGENCY_STOP;
        cmd->priority = 255;
        return true;
    }
    if (strcmp(type, "park") == 0) {
        cmd->type = CMD_TYPE_PARK;
        return true;
    }
    if (strcmp(type, "set_park") == 0) {
        cmd->type = CMD_TYPE_SET_PARK;
        if (!get_float(json, "az_raw", &cmd->park.az_norm)) { return false; }
        if (!get_float(json, "el_raw", &cmd->park.el_norm)) { return false; }
        return true;
    }
    if (strcmp(type, "set_motion") == 0) {
        cmd->type = CMD_TYPE_SET_MOTION;
        char az[8] = {0}, el[8] = {0};
        if (!get_string(json, "az", az, sizeof(az))) { return false; }
        if (!get_string(json, "el", el, sizeof(el))) { return false; }

        if      (strcmp(az, "cw")   == 0) { cmd->motion.az = SM_AZ_CW; }
        else if (strcmp(az, "ccw")  == 0) { cmd->motion.az = SM_AZ_CCW; }
        else if (strcmp(az, "stop") == 0) { cmd->motion.az = SM_AZ_STOP; }
        else { return false; }

        if      (strcmp(el, "up")   == 0) { cmd->motion.el = SM_EL_UP; }
        else if (strcmp(el, "down") == 0) { cmd->motion.el = SM_EL_DOWN; }
        else if (strcmp(el, "stop") == 0) { cmd->motion.el = SM_EL_STOP; }
        else { return false; }
        return true;
    }
    if (strcmp(type, "set_polarization") == 0) {
        cmd->type = CMD_TYPE_SET_POLARIZATION;
        /* Tolerate missing fields — default to false */
        get_bool(json, "pol_vhf",  &cmd->pol.pol_vhf);
        get_bool(json, "pol_uhf",  &cmd->pol.pol_uhf);
        get_bool(json, "lna_uhf",  &cmd->pol.lna_uhf);
        get_bool(json, "rxtx_uhf", &cmd->pol.rxtx_uhf);
        return true;
    }
    if (strcmp(type, "set_limits") == 0) {
        cmd->type = CMD_TYPE_SET_LIMITS;
        if (!get_float(json, "az_min", &cmd->limits.az_min)) { return false; }
        if (!get_float(json, "az_max", &cmd->limits.az_max)) { return false; }
        if (!get_float(json, "el_min", &cmd->limits.el_min)) { return false; }
        if (!get_float(json, "el_max", &cmd->limits.el_max)) { return false; }
        return true;
    }

    if (strcmp(type, "set_netconfig") == 0) {
        cmd->type = CMD_TYPE_SET_NETCONFIG;
        char buf[24];
        /* IP, subnet, gateway are required. */
        if (!get_string(json, "ip",      buf, sizeof(buf))) { return false; }
        unsigned a, b, c, d;
        if (sscanf(buf, "%u.%u.%u.%u", &a, &b, &c, &d) != 4) { return false; }
        cmd->netconfig.ip[0] = (uint8_t)a; cmd->netconfig.ip[1] = (uint8_t)b;
        cmd->netconfig.ip[2] = (uint8_t)c; cmd->netconfig.ip[3] = (uint8_t)d;

        if (!get_string(json, "subnet",  buf, sizeof(buf))) { return false; }
        if (sscanf(buf, "%u.%u.%u.%u", &a, &b, &c, &d) != 4) { return false; }
        cmd->netconfig.subnet[0] = (uint8_t)a; cmd->netconfig.subnet[1] = (uint8_t)b;
        cmd->netconfig.subnet[2] = (uint8_t)c; cmd->netconfig.subnet[3] = (uint8_t)d;

        if (!get_string(json, "gateway", buf, sizeof(buf))) { return false; }
        if (sscanf(buf, "%u.%u.%u.%u", &a, &b, &c, &d) != 4) { return false; }
        cmd->netconfig.gateway[0] = (uint8_t)a; cmd->netconfig.gateway[1] = (uint8_t)b;
        cmd->netconfig.gateway[2] = (uint8_t)c; cmd->netconfig.gateway[3] = (uint8_t)d;

        /* MAC is optional — keep current if absent. */
        cmd->netconfig.has_mac = false;
        if (get_string(json, "mac", buf, sizeof(buf))) {
            unsigned ma, mb, mc, md, me, mf;
            if (sscanf(buf, "%x:%x:%x:%x:%x:%x",
                       &ma, &mb, &mc, &md, &me, &mf) == 6) {
                cmd->netconfig.mac[0] = (uint8_t)ma;
                cmd->netconfig.mac[1] = (uint8_t)mb;
                cmd->netconfig.mac[2] = (uint8_t)mc;
                cmd->netconfig.mac[3] = (uint8_t)md;
                cmd->netconfig.mac[4] = (uint8_t)me;
                cmd->netconfig.mac[5] = (uint8_t)mf;
                cmd->netconfig.has_mac = true;
            }
        }
        return true;
    }
    if (strcmp(type, "reset_netconfig") == 0) {
        cmd->type = CMD_TYPE_RESET_NETCONFIG;
        return true;
    }
    if (strcmp(type, "set_block") == 0) {
        cmd->type = CMD_TYPE_SET_BLOCK;
        float az = 0.0f, el = 0.0f;
        if (!get_float(json, "az_deg",    &az)) { return false; }
        if (!get_float(json, "el_floor",  &el)) { return false; }
        if (az < 0.0f || az > 450.0f || el < 0.0f || el > 180.0f) { return false; }
        cmd->block.az_deg       = az;
        cmd->block.el_floor_deg = (uint8_t)(el + 0.5f);
        return true;
    }
    if (strcmp(type, "set_blocks") == 0) {
        /* Expects "blocks":[v0,v1,...,v89] — 90 uint8 el_floor values in degrees. */
        cmd->type = CMD_TYPE_SET_BLOCKS;
        const char *p = find_val(json, "blocks");
        if (!p || *p != '[') { return false; }
        p++;  /* skip '[' */
        for (uint8_t i = 0; i < AZ_BLOCK_COUNT_CMD; i++) {
            while (*p == ' ') { p++; }
            char *end;
            unsigned long v = strtoul(p, &end, 10);
            if (end == p) { return false; }
            cmd->blocks.el_floor[i] = (v > 180U) ? 180U : (uint8_t)v;
            p = end;
            while (*p == ' ') { p++; }
            if (*p == ',') { p++; }
        }
        return true;
    }
    if (strcmp(type, "reset_blocks") == 0) {
        cmd->type = CMD_TYPE_RESET_BLOCKS;
        return true;
    }
    if (strcmp(type, "reboot") == 0) {
        cmd->type = CMD_TYPE_REBOOT;
        return true;
    }

    return false;   /* unknown type */
}

const char *protocol_encode_ack(uint32_t seq, bool ok, const char *error)
{
    if (ok || !error) {
        snprintf(g_ack_buf, sizeof(g_ack_buf),
                 "{\"type\":\"ack\",\"seq\":%lu,\"ok\":true}\n",
                 (unsigned long)seq);
    } else {
        snprintf(g_ack_buf, sizeof(g_ack_buf),
                 "{\"type\":\"ack\",\"seq\":%lu,\"ok\":false,\"error\":\"%s\"}\n",
                 (unsigned long)seq, error);
    }
    return g_ack_buf;
}

const char *protocol_encode_telemetry(const sm_ctx_t *sm,
                                       float az_raw, float el_raw,
                                       uint32_t ts_ms, uint32_t seq)
{
    char az_str[8], el_str[8];
    fmt_01(az_str, sizeof(az_str), az_raw);
    fmt_01(el_str, sizeof(el_str), el_raw);

    snprintf(g_telem_buf, sizeof(g_telem_buf),
        "{\"type\":\"telemetry\","
        "\"seq\":%lu,"
        "\"ts_ms\":%lu,"
        "\"az_raw\":%s,"
        "\"el_raw\":%s,"
        "\"az_motion\":\"%s\","
        "\"el_motion\":\"%s\","
        "\"pol_vhf\":%s,"
        "\"pol_uhf\":%s,"
        "\"lna_uhf\":%s,"
        "\"rxtx_uhf\":%s,"
        "\"state\":\"%s\","
        "\"fault_detail\":\"\","
        "\"duty_az_pct\":%u,"
        "\"duty_el_pct\":%u}\n",
        (unsigned long)seq,
        (unsigned long)ts_ms,
        az_str, el_str,
        sm_az_str(sm_get_az_motion(sm)),
        sm_el_str(sm_get_el_motion(sm)),
        sm->pol_vhf  ? "true" : "false",
        sm->pol_uhf  ? "true" : "false",
        sm->lna_uhf  ? "true" : "false",
        sm->rxtx_uhf ? "true" : "false",
        sm_state_str(sm_get_state(sm)),
        sm_duty_az_pct(sm),
        sm_duty_el_pct(sm));

    return g_telem_buf;
}
