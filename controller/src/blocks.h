#pragma once
#include <stdint.h>
#include <stdbool.h>

/*
 * AZ-segmented elevation floor (obstacle / no-fly-zone map).
 *
 * The antenna's 450° AZ travel is divided into 90 chunks of 5° each.
 * Each chunk stores a minimum elevation (0–180°, uint8_t degrees).
 * A value of 0 means no restriction for that sector.
 *
 * The el_floor is enforced by the state machine via the sm_input_t.el_floor
 * field: when el_pos ≤ el_floor and the commanded direction is DOWN, the
 * state machine substitutes STOP.
 *
 * Persistence: load/save functions call driverlib EEPROM routines (firmware
 * only).  Everything else (set, reset, get) is pure RAM — safe for host tests.
 */

#define AZ_BLOCK_CHUNK_DEG   5U    /* degrees per chunk  */
#define AZ_BLOCK_COUNT       90U   /* 90 × 5° = 450°     */

/* Initialise in-RAM table from EEPROM.  Call once at boot (after EEPROMInit).
   Returns true if EEPROM held a valid config, false if defaulting to all-zero. */
bool  blocks_load(void);

/* Write current in-RAM table to EEPROM. */
void  blocks_save(void);

/* Set the floor for the 5° chunk that contains az_deg (0–449.9).
   el_floor_deg: minimum elevation in degrees (0 = unrestricted, 180 = fully blocked). */
void  blocks_set(float az_deg, uint8_t el_floor_deg);

/* Set all 90 chunks at once from an external array (used by brain push-on-connect). */
void  blocks_set_all(const uint8_t el_floor[AZ_BLOCK_COUNT]);

/* Zero all chunks (no restrictions).  Does NOT write to EEPROM. */
void  blocks_reset(void);

/* Return the normalised (0..1) minimum elevation for the given normalised AZ.
   Returns 0.0 when no block applies.  Pure RAM — no hardware access. */
float blocks_get_el_floor(float az_norm);

/* Direct read access to the raw table for protocol encoding. */
const uint8_t *blocks_table(void);
