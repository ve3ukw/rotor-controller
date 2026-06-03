#pragma once
#include <stdint.h>
#include <stdbool.h>
#include "wizchip_conf.h"   /* wiz_NetInfo */

/*
 * EEPROM persistence for network configuration.
 *
 * On boot call net_persist_init() then net_persist_load():
 *   - Returns true and fills *out if EEPROM holds a valid override.
 *   - Returns false if EEPROM is blank/invalid — caller uses config.h defaults.
 *
 * Call net_persist_save() whenever the runtime config changes (e.g. via the
 * brain's set_netconfig command) to make the change survive the next reset.
 *
 * Call net_persist_clear() to erase the stored override and revert to factory
 * defaults on the next boot (equivalent to a firmware factory reset).
 */

void net_persist_init(void);
bool net_persist_load(wiz_NetInfo *out);
void net_persist_save(const wiz_NetInfo *cfg);
void net_persist_clear(void);
