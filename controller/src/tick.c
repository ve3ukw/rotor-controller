#include <stdint.h>
#include <stdbool.h>

#include "driverlib/systick.h"
#include "driverlib/interrupt.h"
#include "config.h"
#include "tick.h"

#define SYSTICK_RELOAD  ((SYSCLOCK_HZ / TICK_HZ) - 1U)

static volatile bool     g_tick_pending = false;
static volatile uint32_t g_tick_count   = 0;

/* ISR — must match the SysTick vector table entry in startup_gcc.c.
   Keep this as short as possible: set flag and return. */
void SysTick_Handler(void)
{
    g_tick_pending = true;
    g_tick_count++;
}

void tick_init(void)
{
    SysTickPeriodSet(SYSTICK_RELOAD);
    SysTickIntEnable();
    SysTickEnable();
    IntMasterEnable();
}

bool tick_pending(void)
{
    return g_tick_pending;
}

void tick_clear(void)
{
    g_tick_pending = false;
}

uint32_t tick_count(void)
{
    return g_tick_count;
}
