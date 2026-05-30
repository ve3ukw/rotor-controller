#pragma once
#include <stdint.h>
#include <stdbool.h>

/* Initialise SysTick for TICK_HZ interrupts. Call once before enabling
   global interrupts. */
void     tick_init(void);

/* Returns true if a new tick has fired since the last tick_clear(). */
bool     tick_pending(void);

/* Must be called once at the top of every main-loop iteration. */
void     tick_clear(void);

/* Monotonic tick counter — wraps at UINT32_MAX (~497 days at 100 Hz). */
uint32_t tick_count(void);
