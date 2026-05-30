#pragma once

/*
 * debug_log() — formatted output over UART0 (ICDI virtual COM, 115200 8N1).
 * Compiled out entirely when DEBUG_LOG is not defined — zero overhead in
 * production builds.
 *
 * Enable:  cmake -B build -DENABLE_DEBUG_LOG=ON
 */

#ifdef DEBUG_LOG
#  include "utils/uartstdio.h"
#  define debug_log(fmt, ...) UARTprintf(fmt, ##__VA_ARGS__)
#else
#  define debug_log(fmt, ...) ((void)0)
#endif
