#pragma once
#include <stdint.h>
#include <stdbool.h>
#include "command.h"
#include "state_machine.h"

/*
 * Wire protocol — newline-delimited JSON over TCP (commands) and UDP
 * (telemetry).  All encode functions return a pointer to a static buffer
 * valid until the next call.  Parse functions are re-entrant-safe (no
 * static state).
 *
 * Extended set_polarization (4 switches instead of the brief's 1):
 *   {"type":"set_polarization","seq":N,
 *    "pol_vhf":true|false,"pol_uhf":true|false,
 *    "lna_uhf":true|false,"rxtx_uhf":true|false}
 */

/*
 * Parse one null-terminated JSON line (newline already stripped).
 * On success: populates *cmd and *seq, returns true.
 * On failure: returns false; cmd/seq are unmodified.
 */
bool protocol_parse(const char *json, uint32_t *seq, sm_command_t *cmd);

/*
 * Encode an ack frame.  error may be NULL on success.
 * Returns pointer to static buffer (includes trailing '\n').
 */
const char *protocol_encode_ack(uint32_t seq, bool ok, const char *error);

/*
 * Encode a telemetry frame.
 * ts_ms: milliseconds since boot (tick_count * 10 at 100 Hz).
 * Returns pointer to static buffer (includes trailing '\n').
 */
const char *protocol_encode_telemetry(const sm_ctx_t *sm,
                                      float az_raw, float el_raw,
                                      uint32_t ts_ms, uint32_t seq);
