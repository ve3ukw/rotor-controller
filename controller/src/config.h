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
/* I2C LCD (HD44780 + PCF8574 backpack) on I2C1 — A10=PA6 SCL, A11=PA7 SDA.
   Most modules ship with 0x27; if yours uses A0-A2 jumpers try 0x3F.      */
#define LCD_I2C_ADDR    0x27U
#define LCD_ROWS        4U
#define LCD_COLS        20U

/* Define to reverse the 4 data bits within each nibble.
   Try this if display shows activity but no recognizable characters.
   Some modules wire P4=DB7, P5=DB6, P6=DB5, P7=DB4 (reversed order).
   Leave undefined for standard P4=DB4, P5=DB5, P6=DB6, P7=DB7 wiring. */
/* #define LCD_NIBBLE_REVERSED */

/* PCF8574A → HD44780 bit assignments (standard PCF8574A wiring).
 * Control: P0=RS, P1=RW, P2=EN, P3=BL
 * Data:    P4=DB7, P5=DB6, P6=DB5, P7=DB4  ← reversed data bus!
 * LCD_NIBBLE_REVERSED compensates for the reversed data bus.    */
#define PCF_RS   0x01   /* P0 = RS */
#define PCF_RW   0x02   /* P1 = RW */
#define PCF_EN   0x04   /* P2 = EN */
#define PCF_BL   0x08   /* P3 = BL */

#define PARK_AZ_NORM    0.400f
#define PARK_EL_NORM    0.250f
#define PARK_TOLERANCE  0.005f   /* dead-band ≈ 2.25° az / 0.9° el */
