#pragma once
#include <stdint.h>

/* System clock — must match SysCtlClockSet() in main(). */
#define SYSCLOCK_HZ     80000000UL

/* 100 Hz main tick. */
#define TICK_HZ         100U

/* ── Network defaults ─────────────────────────────────────────────────────
 * Edit these to relocate the field unit on your network.  The brain can
 * also push a new config at runtime via net_set_config() — changes are
 * effective immediately but lost on reset (no EEPROM).
 * ───────────────────────────────────────────────────────────────────────── */
#define NET_MAC       { 0x02, 0x00, 0xDC, 0x57, 0x3A, 0xF2 }
#define NET_IP        { 192, 168, 3,   1 }
#define NET_SUBNET    { 255, 255, 255, 0 }
#define NET_GATEWAY   { 192, 168, 3, 254 }
#define NET_DNS       {   0,   0,   0, 0 }   /* unused */

#define NET_TCP_PORT  7700U   /* command socket */
#define NET_UDP_PORT  7701U   /* telemetry socket */

/* SPI clock for W5500 (max 80 MHz; use 20 MHz for margin) */
#define NET_SPI_HZ    20000000UL

/* ── Parking position ─────────────────────────────────────────────────────
 * Normalized 0..1 coordinates assuming G-5500 full travel:
 *   Az: 0–450° → 180° / 450° = 0.400  (south-facing)
 *   El: 0–180° →  45° / 180° = 0.250  (45° elevation)
 * Adjust if actual pot calibration differs from full-range assumption. */
#define PARK_AZ_NORM    0.400f
#define PARK_EL_NORM    0.250f
#define PARK_TOLERANCE  0.005f   /* dead-band ≈ 2.25° az / 0.9° el */
