#include <stdint.h>
#include <stdbool.h>
#include <string.h>
#include "driverlib/eeprom.h"
#include "driverlib/sysctl.h"
#include "net_persist.h"
#include "debug.h"

/* Magic word written at offset 0 to mark a valid stored config.
   Change this value to invalidate all stored configs after a struct change. */
#define NET_EEPROM_MAGIC    0xA15EB00CUL
#define NET_EEPROM_ADDR     0x00000000UL  /* byte offset within EEPROM */

/* Stored layout — must be 32-bit aligned, size a multiple of 4 bytes.
   EEPROMRead/Program operate in 32-bit words so we pad to 36 bytes. */
typedef struct {
    uint32_t magic;       /*  0: NET_EEPROM_MAGIC when valid          */
    uint8_t  ip[4];       /*  4: override IP address                  */
    uint8_t  subnet[4];   /*  8: override subnet mask                 */
    uint8_t  gateway[4];  /* 12: override default gateway             */
    uint8_t  mac[6];      /* 16: override MAC (locally administered)  */
    uint8_t  _pad[2];     /* 22: align to 4 bytes                     */
    uint8_t  _reserved[8];/* 24: reserved for future fields           */
                          /* 32: total — 8 EEPROM words               */
} __attribute__((packed, aligned(4))) net_eeprom_t;

_Static_assert(sizeof(net_eeprom_t) == 32, "net_eeprom_t size mismatch");
_Static_assert(sizeof(net_eeprom_t) % 4 == 0, "net_eeprom_t must be 4-byte multiple");

void net_persist_init(void)
{
    SysCtlPeripheralEnable(SYSCTL_PERIPH_EEPROM0);
    while (!SysCtlPeripheralReady(SYSCTL_PERIPH_EEPROM0)) {}
    EEPROMInit();  /* required after peripheral enable */
}

bool net_persist_load(wiz_NetInfo *out)
{
    net_eeprom_t stored;
    EEPROMRead((uint32_t *)&stored, NET_EEPROM_ADDR, sizeof(stored));

    if (stored.magic != NET_EEPROM_MAGIC) {
        debug_log("netcfg: EEPROM blank — using factory defaults\r\n");
        return false;
    }

    memcpy(out->ip,  stored.ip,      4);
    memcpy(out->sn,  stored.subnet,  4);
    memcpy(out->gw,  stored.gateway, 4);
    memcpy(out->mac, stored.mac,     6);

    debug_log("netcfg: EEPROM → IP=%u.%u.%u.%u\r\n",
              out->ip[0], out->ip[1], out->ip[2], out->ip[3]);
    return true;
}

void net_persist_save(const wiz_NetInfo *cfg)
{
    net_eeprom_t data;
    memset(&data, 0, sizeof(data));
    data.magic = NET_EEPROM_MAGIC;
    memcpy(data.ip,      cfg->ip,  4);
    memcpy(data.subnet,  cfg->sn,  4);
    memcpy(data.gateway, cfg->gw,  4);
    memcpy(data.mac,     cfg->mac, 6);

    EEPROMProgram((uint32_t *)&data, NET_EEPROM_ADDR, sizeof(data));
    debug_log("netcfg: saved to EEPROM IP=%u.%u.%u.%u\r\n",
              cfg->ip[0], cfg->ip[1], cfg->ip[2], cfg->ip[3]);
}

void net_persist_clear(void)
{
    /* Write zero over the magic word — next boot uses factory defaults. */
    uint32_t invalid = 0x00000000UL;
    EEPROMProgram(&invalid, NET_EEPROM_ADDR, sizeof(uint32_t));
    debug_log("netcfg: EEPROM cleared — factory defaults on next boot\r\n");
}
