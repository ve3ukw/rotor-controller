#include <stdint.h>
#include <stdbool.h>

#include "driverlib/gpio.h"
#include "driverlib/sysctl.h"
#include "inc/hw_gpio.h"
#include "inc/hw_memmap.h"
#include "inc/hw_types.h"

#include "gpio_outputs.h"

/* ── pin assignments ──────────────────────────────────────────────────────── */

/* Port F */
#define PF_MOTOR_UP     GPIO_PIN_4   /* A1 */
#define PF_POL_VHF      GPIO_PIN_0   /* B1  — NMI pin, needs commit-register unlock */

/* Port E */
#define PE_MOTOR_DOWN   GPIO_PIN_0   /* A2 */
#define PE_MOTOR_CW     GPIO_PIN_3   /* A3 */
#define PE_MOTOR_CCW    GPIO_PIN_4   /* A4 */

/* Port C (PC0–PC3 are JTAG — do not touch) */
#define PC_POL_UHF      GPIO_PIN_4   /* B2 */
#define PC_LNA_UHF      GPIO_PIN_5   /* B3 */
#define PC_RXTX_UHF     GPIO_PIN_6   /* B4 */

/* ── init ─────────────────────────────────────────────────────────────────── */

void gpio_outputs_init(void)
{
    SysCtlPeripheralEnable(SYSCTL_PERIPH_GPIOC);
    SysCtlPeripheralEnable(SYSCTL_PERIPH_GPIOE);
    SysCtlPeripheralEnable(SYSCTL_PERIPH_GPIOF);
    while (!SysCtlPeripheralReady(SYSCTL_PERIPH_GPIOC)) {}
    while (!SysCtlPeripheralReady(SYSCTL_PERIPH_GPIOE)) {}
    /* GPIOF ready is not checked here: main() already waited on it for LEDs. */

    /* PF0 is the NMI pin and is locked by the hardware on EK-TM4C123GXL.
       Write the unlock key to GPIOLOCK, set the commit bit, then relock. */
    HWREG(GPIO_PORTF_BASE + GPIO_O_LOCK) = GPIO_LOCK_KEY;
    HWREG(GPIO_PORTF_BASE + GPIO_O_CR)  |= PF_POL_VHF;
    HWREG(GPIO_PORTF_BASE + GPIO_O_LOCK) = 0;

    GPIOPinTypeGPIOOutput(GPIO_PORTF_BASE, PF_MOTOR_UP | PF_POL_VHF);
    GPIOPinTypeGPIOOutput(GPIO_PORTE_BASE, PE_MOTOR_DOWN | PE_MOTOR_CW | PE_MOTOR_CCW);
    GPIOPinTypeGPIOOutput(GPIO_PORTC_BASE, PC_POL_UHF | PC_LNA_UHF | PC_RXTX_UHF);

    /* Bump drive strength from the 2 mA default to the part's max 8 mA —
       these pins drive opto-isolator LEDs, which need several mA of forward
       current to push the output-side phototransistor into saturation. */
    GPIOPadConfigSet(GPIO_PORTF_BASE, PF_MOTOR_UP | PF_POL_VHF,
                     GPIO_STRENGTH_8MA, GPIO_PIN_TYPE_STD);
    GPIOPadConfigSet(GPIO_PORTE_BASE, PE_MOTOR_DOWN | PE_MOTOR_CW | PE_MOTOR_CCW,
                     GPIO_STRENGTH_8MA, GPIO_PIN_TYPE_STD);
    GPIOPadConfigSet(GPIO_PORTC_BASE, PC_POL_UHF | PC_LNA_UHF | PC_RXTX_UHF,
                     GPIO_STRENGTH_8MA, GPIO_PIN_TYPE_STD);

    gpio_outputs_safe();
}

/* ── safe state ───────────────────────────────────────────────────────────── */

void gpio_outputs_safe(void)
{
    GPIOPinWrite(GPIO_PORTF_BASE, PF_MOTOR_UP  | PF_POL_VHF,                    0);
    GPIOPinWrite(GPIO_PORTE_BASE, PE_MOTOR_DOWN | PE_MOTOR_CW | PE_MOTOR_CCW,   0);
    GPIOPinWrite(GPIO_PORTC_BASE, PC_POL_UHF   | PC_LNA_UHF  | PC_RXTX_UHF,    0);
}

/* ── motor contacts (state machine only) ─────────────────────────────────── */

void gpio_motor_az_set(motor_az_t dir)
{
    uint8_t val = 0;
    if (dir == MOTOR_AZ_CW)  { val = PE_MOTOR_CW; }
    if (dir == MOTOR_AZ_CCW) { val = PE_MOTOR_CCW; }
    GPIOPinWrite(GPIO_PORTE_BASE, PE_MOTOR_CW | PE_MOTOR_CCW, val);
}

void gpio_motor_el_set(motor_el_t dir)
{
    uint8_t pf_val = 0;
    uint8_t pe_val = 0;
    if (dir == MOTOR_EL_UP)   { pf_val = PF_MOTOR_UP; }
    if (dir == MOTOR_EL_DOWN) { pe_val = PE_MOTOR_DOWN; }
    GPIOPinWrite(GPIO_PORTF_BASE, PF_MOTOR_UP,    pf_val);
    GPIOPinWrite(GPIO_PORTE_BASE, PE_MOTOR_DOWN,  pe_val);
}

/* ── RF / antenna switches ────────────────────────────────────────────────── */

void gpio_pol_vhf_set(bool active)
{
    GPIOPinWrite(GPIO_PORTF_BASE, PF_POL_VHF, active ? PF_POL_VHF : 0);
}

void gpio_pol_uhf_set(bool active)
{
    GPIOPinWrite(GPIO_PORTC_BASE, PC_POL_UHF, active ? PC_POL_UHF : 0);
}

void gpio_lna_uhf_set(bool active)
{
    GPIOPinWrite(GPIO_PORTC_BASE, PC_LNA_UHF, active ? PC_LNA_UHF : 0);
}

void gpio_rxtx_uhf_set(bool active)
{
    GPIOPinWrite(GPIO_PORTC_BASE, PC_RXTX_UHF, active ? PC_RXTX_UHF : 0);
}
