#pragma once
#include <stdint.h>
#include <stdbool.h>

/*
 * ADC pipeline — az and el pot wipers via ADC0 SS0.
 *
 * Hardware:  AIN4 (PD3) = azimuth,  AIN2 (PE1) = elevation.
 * Timer1A triggers SS0 at 1 kHz.  uDMA (ping-pong) fills a ring buffer
 * in SRAM.  Main loop calls adc_tick() once per tick then reads the
 * filtered values.  No blocking in the read path.
 *
 * Returned values are normalized 0.0–1.0 (raw ADC counts / 4095).
 * The field unit never converts to degrees; that is brain-tier.
 */

void  adc_init(void);

/* Must be called once per main-loop tick to manage the plausibility window. */
void  adc_tick(void);

/* Filtered, plausibility-checked reading.  Returns the previous value on a
   rejected sample; returns 0.0 before the ring is primed. */
float adc_get_az(void);
float adc_get_el(void);

/* True until too many plausibility rejects have accumulated in the window. */
bool  adc_is_valid(void);

/* Called by the state machine on a clear_fault command. */
void  adc_clear_fault(void);
