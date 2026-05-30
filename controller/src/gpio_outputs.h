#pragma once
#include <stdbool.h>

/*
 * Abstract motor contact closures and RF/antenna switch outputs.
 *
 * Motor contacts (CW/CCW/UP/DOWN) are written ONLY by the state machine —
 * never by the command parser or network layer.
 *
 * RF control outputs (pol_vhf, pol_uhf, lna_uhf, rxtx_uhf) are driven by
 * brain commands independently of motor motion.
 *
 * Pin assignments (BOOSTXL-IOBKOUT):
 *   UP     PF4  A1       CW      PE3  A3
 *   DOWN   PE0  A2       CCW     PE4  A4
 *   POL_VHF  PF0  B1     POL_UHF  PC4  B2
 *   LNA_UHF  PC5  B3     RXTX_UHF PC6  B4
 */

typedef enum {
    MOTOR_AZ_STOP = 0,
    MOTOR_AZ_CW,
    MOTOR_AZ_CCW,
} motor_az_t;

typedef enum {
    MOTOR_EL_STOP = 0,
    MOTOR_EL_UP,
    MOTOR_EL_DOWN,
} motor_el_t;

/* Call once at boot, before any other function in this module. */
void gpio_outputs_init(void);

/* De-energise every output.  Called on boot and on any fault. */
void gpio_outputs_safe(void);

/* Motor contacts — state machine eyes only. */
void gpio_motor_az_set(motor_az_t dir);
void gpio_motor_el_set(motor_el_t dir);

/* RF/antenna switches — brain-commandable, independent of motion. */
void gpio_pol_vhf_set(bool active);
void gpio_pol_uhf_set(bool active);
void gpio_lna_uhf_set(bool active);
void gpio_rxtx_uhf_set(bool active);
