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
#include "blocks.h"
#include "display.h"
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

    /* Configure watchdog timer but DON'T enable yet — hardware init sequences
       (LCD 50ms power-on, W5500 reset, etc.) take longer than 100ms and would
       trigger a reset loop.  Watchdog is armed after all init completes. */
    SysCtlPeripheralEnable(SYSCTL_PERIPH_WDOG0);
    while (!SysCtlPeripheralReady(SYSCTL_PERIPH_WDOG0)) {}
    WatchdogReloadSet(WATCHDOG0_BASE, SYSCLOCK_HZ / 2);   /* 500 ms — headroom for EEPROM writes (24 words × 4 ms = 96 ms max) */
    WatchdogResetEnable(WATCHDOG0_BASE);

    tick_init();
    adc_init();
    gpio_outputs_init();

#ifdef DEBUG_LOG
    /* LED self-test: hold the A8 park button (PB0) low at boot to cycle
       through all 4 motor-direction outputs (AZ CW/CCW, EL UP/DOWN), 1 s on
       / 1 s off each, so the LEDs wired in parallel with the FETs can be
       checked visually. 1s pulses are short enough to be safe even with the
       rotor connected. PB0 is a dedicated input (own pull-up, never
       repurposed) and is re-checked every cycle, so releasing it falls
       through to normal operation without needing a reset. */
    if (GPIOPinRead(GPIO_PORTB_BASE, GPIO_PIN_0) == 0) {
        debug_log("LED TEST: cycling AZ CW / AZ CCW / EL UP / EL DOWN, "
                   "release A8 to exit\r\n");
        while (GPIOPinRead(GPIO_PORTB_BASE, GPIO_PIN_0) == 0) {
            debug_log("LED TEST: AZ CW\r\n");
            gpio_motor_az_set(MOTOR_AZ_CW);
            SysCtlDelay(SYSCLOCK_HZ / 3);
            gpio_motor_az_set(MOTOR_AZ_STOP);
            SysCtlDelay(SYSCLOCK_HZ / 3);

            debug_log("LED TEST: AZ CCW\r\n");
            gpio_motor_az_set(MOTOR_AZ_CCW);
            SysCtlDelay(SYSCLOCK_HZ / 3);
            gpio_motor_az_set(MOTOR_AZ_STOP);
            SysCtlDelay(SYSCLOCK_HZ / 3);

            debug_log("LED TEST: EL UP\r\n");
            gpio_motor_el_set(MOTOR_EL_UP);
            SysCtlDelay(SYSCLOCK_HZ / 3);
            gpio_motor_el_set(MOTOR_EL_STOP);
            SysCtlDelay(SYSCLOCK_HZ / 3);

            debug_log("LED TEST: EL DOWN\r\n");
            gpio_motor_el_set(MOTOR_EL_DOWN);
            SysCtlDelay(SYSCLOCK_HZ / 3);
            gpio_motor_el_set(MOTOR_EL_STOP);
            SysCtlDelay(SYSCLOCK_HZ / 3);
        }
        gpio_outputs_safe();
        debug_log("LED TEST: done\r\n");
    }
#endif

    net_init();          /* calls net_persist_init() which enables EEPROM peripheral */
    blocks_load();       /* load AZ block floors from EEPROM (EEPROM already up) */
    display_init();
    display_splash("  Rotor Controller  ",
                   "     Starting...    ",
                   "                    ",
                   "     VE3UKW         ");

    /* Arm the watchdog AFTER all init — kicked every tick in the main loop.
       On timeout: IntDefaultHandler spins → second timeout → system reset → safe state. */
    WatchdogEnable(WATCHDOG0_BASE);
    WatchdogLock(WATCHDOG0_BASE);

    sm_ctx_t sm;
    sm_init(&sm);

    uint32_t ticks_since_toggle = 0;
    bool     park_prev = true;             /* A8 (PB0) previous state for edge detect */

    for (;;) {
        while (!tick_pending()) {}
        tick_clear();
        WatchdogIntClear(WATCHDOG0_BASE);   /* kick hardware watchdog */

        /* ── Hardware safety inputs ─────────────────────────────────────── */
        bool estop_low = (GPIOPinRead(GPIO_PORTB_BASE, GPIO_PIN_1) == 0);  /* A9 PB1 */
        bool park_low  = (GPIOPinRead(GPIO_PORTB_BASE, GPIO_PIN_0) == 0);  /* A8 PB0 */

        static bool estop_was_low = false;
        if (estop_low) {
            /* A9 (PB1): emergency stop held low — push every tick while asserted.
               Priority 255 means no brain command can override while pin is held.
               If stuck in ESTOP: check that nothing from G-5500 wiring is
               pulling PB1 to GND — it should only be driven by the ESTOP button. */
            if (!estop_was_low) {
                debug_log("ESTOP: hardware A9 asserted\r\n");
            }
            sm_command_t estop = { .type     = CMD_TYPE_EMERGENCY_STOP,
                                   .source   = CMD_SRC_LOCAL,
                                   .priority = 255 };
            sm_push_command(&sm, &estop);
        } else if (estop_was_low) {
            debug_log("ESTOP: hardware A9 released\r\n");
        }
        estop_was_low = estop_low;

        /* A8: falling edge = park trigger; held 3 s while ESTOP latched = acknowledge.
           The 3-second hold provides a hardware-only ESTOP clear when the brain is
           not connected (e.g. bench testing without rotor-brain running). */
        static uint32_t park_held_ticks = 0;
        if (!estop_low && sm.estop_hw_latch && park_low) {
            park_held_ticks++;
            if (park_held_ticks >= TICK_HZ * 3U) {   /* 3 seconds */
                park_held_ticks = 0;
                sm.estop_hw_latch = false;
                sm.estop_active   = false;
                debug_log("ESTOP: latch cleared via A8 hold\r\n");
            }
        } else {
            if (park_low && park_prev && !sm.estop_hw_latch) {
                /* Normal park trigger — only when not in ESTOP latch */
                sm_command_t park = { .type     = CMD_TYPE_PARK,
                                      .source   = CMD_SRC_LOCAL,
                                      .priority = 10 };
                sm_push_command(&sm, &park);
            }
            park_held_ticks = 0;
        }
        park_prev = !park_low;   /* track for next tick */

        adc_tick();

        float az = adc_get_az();
        sm_input_t in = {
            .az_pos    = az,
            .el_pos    = adc_get_el(),
            .adc_valid = adc_is_valid(),
            .el_floor  = blocks_get_el_floor(az),
        };

        sm_output_t out = sm_tick(&sm, &in);
        apply_outputs(&out);

        net_tick(&sm, in.az_pos, in.el_pos, tick_count() * (1000U / TICK_HZ));
        display_tick(&sm, in.az_pos, in.el_pos);

        /* Red LED: brain TCP session active. */
        GPIOPinWrite(GPIO_PORTF_BASE, LED_RED,
            net_is_connected() ? LED_RED : 0);

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
