#pragma once
#include <stdint.h>
#include <stdbool.h>

/*
 * Abstract command type — every command source (TCP brain, future GS-232 UART,
 * etc.) maps its messages into this struct before handing them to the state
 * machine.  Adding a new source is purely additive: implement a parser that
 * produces sm_command_t values and calls sm_push_command().
 */

typedef enum {
    CMD_TYPE_HELLO = 0,
    CMD_TYPE_HEARTBEAT,
    CMD_TYPE_SET_MOTION,
    CMD_TYPE_SET_POLARIZATION,
    CMD_TYPE_SET_LIMITS,
    CMD_TYPE_CLEAR_FAULT,
    CMD_TYPE_EMERGENCY_STOP,
    CMD_TYPE_PARK,          /* move to pre-defined parking position      */
    CMD_TYPE_SET_NETCONFIG, /* override IP/subnet/gateway/MAC in EEPROM  */
    CMD_TYPE_RESET_NETCONFIG, /* clear EEPROM override → factory defaults */
} cmd_type_t;

typedef enum {
    CMD_SRC_TCP   = 0,  /* v1: primary brain connection */
    CMD_SRC_UART  = 1,  /* future: GS-232 / Hamlib shim */
    CMD_SRC_LOCAL = 2,  /* hardware inputs (A10 park, A11 e-stop) */
} cmd_source_t;

/* Motion values match sm_az_motion_t / sm_el_motion_t exactly. */
typedef struct {
    uint8_t az;   /* 0 = stop, 1 = cw,  2 = ccw */
    uint8_t el;   /* 0 = stop, 1 = up,  2 = down */
} cmd_motion_t;

typedef struct {
    bool pol_vhf;
    bool pol_uhf;
    bool lna_uhf;
    bool rxtx_uhf;
} cmd_polarization_t;

typedef struct {
    float az_min, az_max;   /* normalized 0..1 */
    float el_min, el_max;
} cmd_limits_t;

typedef struct {
    uint8_t ip[4];
    uint8_t subnet[4];
    uint8_t gateway[4];
    uint8_t mac[6];
    bool    has_mac;    /* false = keep current MAC */
} cmd_netconfig_t;

typedef struct {
    cmd_type_t   type;
    cmd_source_t source;
    uint8_t      priority;  /* higher = wins on conflict */
    union {
        cmd_motion_t      motion;
        cmd_polarization_t pol;
        cmd_limits_t      limits;
        cmd_netconfig_t   netconfig;
    };
} sm_command_t;
