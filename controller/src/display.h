#pragma once
#include <stdbool.h>
#include "state_machine.h"

/*
 * 4×20 character LCD via I2C (HD44780 + PCF8574 backpack).
 * Hardware: I2C1 — A10 (PA6) = SCL, A11 (PA7) = SDA.
 * Address and dimensions in config.h (LCD_I2C_ADDR, LCD_ROWS, LCD_COLS).
 *
 * If no display is detected on the bus, all subsequent calls are no-ops.
 *
 * Screen layout:
 *   Row 0  AZ:0.400  [>> CW  ]
 *   Row 1  EL:0.250  [^^ UP  ]
 *   Row 2  IDLE        LINKED
 *   Row 3  A:  0% E:  0% V-U-L-
 */

/* Call once after system clock setup (before watchdog is armed). */
void display_init(void);

/* Write a static 4-row message immediately — useful during boot before the
   main loop starts.  Each string is truncated/padded to LCD_COLS chars. */
void display_splash(const char *row0, const char *row1,
                    const char *row2, const char *row3);

/* Call every main-loop tick.  Refreshes all 4 rows once per second (every
   100 ticks) in a single burst so rows don't update at staggered times. */
void display_tick(const sm_ctx_t *sm, float az_raw, float el_raw);
