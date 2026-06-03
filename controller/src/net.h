#pragma once
#include <stdint.h>
#include <stdbool.h>
#include "state_machine.h"

/*
 * Network layer — W5500 Ethernet via SSI0.
 *
 * Hardware pins (BOOSTXL-IOBKOUT):
 *   CLK  PA2  B11     MOSI  PA5  B8
 *   MISO PA4  B9      CS    PA3  B10  (software GPIO)
 *   RST  PD7  B7  (NMI-locked, unlocked at init)
 *   INT  PD6  B6  (input; polled in step 6, interrupt-driven in step 8)
 *
 * Sockets:
 *   0 — TCP, port NET_TCP_PORT  (commands / acks)
 *   1 — UDP, port NET_UDP_PORT  (telemetry blast)
 */

/* Runtime IP reconfiguration.  Changes take effect immediately on the W5500
 * but are not persisted across resets — the brain should push config on each
 * reconnect.  All arrays are big-endian (network byte order). */
typedef struct {
    uint8_t mac[6];
    uint8_t ip[4];
    uint8_t subnet[4];
    uint8_t gateway[4];
} net_config_t;

void net_init(void);

/* Returns true while a brain TCP session is active. */
bool net_is_connected(void);

/*
 * Call once per main-loop tick.  Handles TCP command receive + ack,
 * UDP telemetry emission (~20 Hz), and socket lifecycle.
 */
void net_tick(const sm_ctx_t *sm, float az_raw, float el_raw, uint32_t ts_ms);

void net_set_config(const net_config_t *cfg);
