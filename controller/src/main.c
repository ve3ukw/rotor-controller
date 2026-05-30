#include <stdint.h>
#include <stdbool.h>

#include "driverlib/sysctl.h"
#include "driverlib/gpio.h"
#include "driverlib/watchdog.h"
#include "inc/hw_memmap.h"

#include "config.h"
#include "tick.h"
#include "adc.h"
#include "gpio_outputs.h"
#include "state_machine.h"
#include "net.h"
#include "w5500.h"    /* getSn_SR — temporary diagnostic */
#include "debug.h"

#ifdef DEBUG_LOG
#include "driverlib/uart.h"
#include "driverlib/pin_map.h"
#include "utils/uartstdio.h"
static void debug_uart_init(void)
{
    SysCtlPeripheralEnable(SYSCTL_PERIPH_UART0);
    SysCtlPeripheralEnable(SYSCTL_PERIPH_GPIOA);
    GPIOPinConfigure(GPIO_PA0_U0RX);
    GPIOPinConfigure(GPIO_PA1_U0TX);
    GPIOPinTypeUART(GPIO_PORTA_BASE, GPIO_PIN_0 | GPIO_PIN_1);
    UARTStdioConfig(0, 115200, SYSCLOCK_HZ);
}
#endif

/* EK-TM4C123GXL on-board RGB LED — Port F, active-high */
#define LED_RED   GPIO_PIN_1
#define LED_BLUE  GPIO_PIN_2
#define LED_GREEN GPIO_PIN_3

#define TICKS_PER_LED_TOGGLE  50U   /* 500 ms → 1 Hz blue blink */

static void apply_outputs(const sm_output_t *out)
{
    gpio_motor_az_set((motor_az_t)out->az_dir);
    gpio_motor_el_set((motor_el_t)out->el_dir);
    gpio_pol_vhf_set(out->pol_vhf);
    gpio_pol_uhf_set(out->pol_uhf);
    gpio_lna_uhf_set(out->lna_uhf);
    gpio_rxtx_uhf_set(out->rxtx_uhf);
}

int main(void)
{
    /* 16 MHz crystal → PLL → 80 MHz */
    SysCtlClockSet(SYSCTL_SYSDIV_2_5 | SYSCTL_USE_PLL |
                   SYSCTL_OSC_MAIN   | SYSCTL_XTAL_16MHZ);

    /* A9 (PB1) = hardware emergency stop   Hi = run, Lo = stop
       A8 (PB0) = hardware park trigger     falling edge = park */
    SysCtlPeripheralEnable(SYSCTL_PERIPH_GPIOB);
    while (!SysCtlPeripheralReady(SYSCTL_PERIPH_GPIOB)) {}
    GPIOPinTypeGPIOInput(GPIO_PORTB_BASE, GPIO_PIN_0 | GPIO_PIN_1);
    GPIOPadConfigSet(GPIO_PORTB_BASE, GPIO_PIN_0 | GPIO_PIN_1,
                     GPIO_STRENGTH_2MA, GPIO_PIN_TYPE_STD_WPU);

#ifdef DEBUG_LOG
    debug_uart_init();
    debug_log("boot\r\n");
#endif

    SysCtlPeripheralEnable(SYSCTL_PERIPH_GPIOF);
    while (!SysCtlPeripheralReady(SYSCTL_PERIPH_GPIOF)) {}

    GPIOPinTypeGPIOOutput(GPIO_PORTF_BASE, LED_RED | LED_BLUE | LED_GREEN);
    GPIOPinWrite(GPIO_PORTF_BASE, LED_RED | LED_BLUE | LED_GREEN, 0);

    /* Hardware watchdog — 100ms reload, reset on second timeout (≤200ms to reset).
       IntDefaultHandler in startup_gcc.c spins on first timeout; second timeout
       fires the system reset.  On boot, gpio_outputs_safe() ensures safe state. */
    SysCtlPeripheralEnable(SYSCTL_PERIPH_WDOG0);
    while (!SysCtlPeripheralReady(SYSCTL_PERIPH_WDOG0)) {}
    WatchdogReloadSet(WATCHDOG0_BASE, SYSCLOCK_HZ / 10);  /* 100 ms */
    WatchdogResetEnable(WATCHDOG0_BASE);
    WatchdogEnable(WATCHDOG0_BASE);
    WatchdogLock(WATCHDOG0_BASE);

    tick_init();
    adc_init();
    gpio_outputs_init();
    net_init();

    sm_ctx_t sm;
    sm_init(&sm);

    uint32_t ticks_since_toggle = 0;
    bool     a10_prev = true;              /* A10 previous state for edge detect */

    for (;;) {
        while (!tick_pending()) {}
        tick_clear();
        WatchdogIntClear(WATCHDOG0_BASE);   /* kick hardware watchdog */

        /* ── Hardware safety inputs ─────────────────────────────────────── */
        bool a11_low = (GPIOPinRead(GPIO_PORTB_BASE, GPIO_PIN_1) == 0);  /* A9 */
        bool a10_low = (GPIOPinRead(GPIO_PORTB_BASE, GPIO_PIN_0) == 0);  /* A8 */

        if (a11_low) {
            /* A11: emergency stop held — push every tick while low */
            sm_command_t estop = { .type     = CMD_TYPE_EMERGENCY_STOP,
                                   .source   = CMD_SRC_LOCAL,
                                   .priority = 255 };
            sm_push_command(&sm, &estop);
        }

        if (a10_low && a10_prev) {
            /* A10: falling edge — trigger park once per press */
            sm_command_t park = { .type     = CMD_TYPE_PARK,
                                  .source   = CMD_SRC_LOCAL,
                                  .priority = 10 };
            sm_push_command(&sm, &park);
        }
        a10_prev = !a10_low;   /* track for next tick */

        adc_tick();

        sm_input_t in = {
            .az_pos    = adc_get_az(),
            .el_pos    = adc_get_el(),
            .adc_valid = adc_is_valid(),
        };

        sm_output_t out = sm_tick(&sm, &in);
        apply_outputs(&out);

        net_tick(&sm, in.az_pos, in.el_pos, tick_count() * (1000U / TICK_HZ));

        /* Diagnostic: red LED = TCP socket is active (listening or connected). */
        {
            uint8_t _sr = getSn_SR(0);
            GPIOPinWrite(GPIO_PORTF_BASE, LED_RED,
                (_sr == SOCK_LISTEN || _sr == SOCK_ESTABLISHED)
                ? LED_RED : 0);
        }

        /* Blue LED: 1 Hz blink — scheduler alive. */
        if (++ticks_since_toggle >= TICKS_PER_LED_TOGGLE) {
            ticks_since_toggle = 0;
            uint8_t cur = GPIOPinRead(GPIO_PORTF_BASE, LED_BLUE);
            GPIOPinWrite(GPIO_PORTF_BASE, LED_BLUE, cur ^ LED_BLUE);
        }

        /* Green LED: no active fault. */
        GPIOPinWrite(GPIO_PORTF_BASE, LED_GREEN,
            (sm_get_state(&sm) == SM_STATE_IDLE ||
             sm_get_state(&sm) == SM_STATE_MOVING) ? LED_GREEN : 0);
    }
}
