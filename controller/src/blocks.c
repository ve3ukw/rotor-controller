#include <stdint.h>
#include <stdbool.h>
#include <string.h>

#include "driverlib/eeprom.h"
#include "blocks.h"
#include "debug.h"

#define BLOCKS_MAGIC       0xB10C5AFEUL
#define BLOCKS_EEPROM_ADDR 0x00000020UL   /* byte offset — after net_persist's 32 bytes */

/* Stored layout: 4 magic + 90 data + 2 pad = 96 bytes (24 EEPROM words). */
typedef struct {
    uint32_t magic;
    uint8_t  el_floor[AZ_BLOCK_COUNT];
    uint8_t  _pad[2];
} __attribute__((packed, aligned(4))) blocks_eeprom_t;

_Static_assert(sizeof(blocks_eeprom_t) == 96, "blocks_eeprom_t size mismatch");
_Static_assert(sizeof(blocks_eeprom_t) % 4 == 0, "blocks_eeprom_t must be 4-byte multiple");

/* In-RAM table — only blocks_get_el_floor() reads this; no hardware access. */
static uint8_t g_blocks[AZ_BLOCK_COUNT];

bool blocks_load(void)
{
    memset(g_blocks, 0, sizeof(g_blocks));

    blocks_eeprom_t stored;
    EEPROMRead((uint32_t *)&stored, BLOCKS_EEPROM_ADDR, sizeof(stored));

    if (stored.magic != BLOCKS_MAGIC) {
        debug_log("blocks: EEPROM blank — all sectors open\r\n");
        return false;
    }
    memcpy(g_blocks, stored.el_floor, AZ_BLOCK_COUNT);
    debug_log("blocks: loaded from EEPROM\r\n");
    return true;
}

void blocks_save(void)
{
    blocks_eeprom_t data;
    memset(&data, 0, sizeof(data));
    data.magic = BLOCKS_MAGIC;
    memcpy(data.el_floor, g_blocks, AZ_BLOCK_COUNT);
    EEPROMProgram((uint32_t *)&data, BLOCKS_EEPROM_ADDR, sizeof(data));
    debug_log("blocks: saved to EEPROM\r\n");
}

void blocks_set(float az_deg, uint8_t el_floor_deg)
{
    if (az_deg < 0.0f)   az_deg = 0.0f;
    if (az_deg > 449.9f) az_deg = 449.9f;
    uint8_t idx = (uint8_t)(az_deg / (float)AZ_BLOCK_CHUNK_DEG);
    if (idx >= AZ_BLOCK_COUNT) idx = AZ_BLOCK_COUNT - 1U;
    g_blocks[idx] = el_floor_deg;
}

void blocks_set_all(const uint8_t el_floor[AZ_BLOCK_COUNT])
{
    memcpy(g_blocks, el_floor, AZ_BLOCK_COUNT);
}

void blocks_reset(void)
{
    memset(g_blocks, 0, sizeof(g_blocks));
}

float blocks_get_el_floor(float az_norm)
{
    float az_deg = az_norm * 450.0f;
    if (az_deg < 0.0f) az_deg = 0.0f;
    uint8_t idx = (uint8_t)(az_deg / (float)AZ_BLOCK_CHUNK_DEG);
    if (idx >= AZ_BLOCK_COUNT) idx = AZ_BLOCK_COUNT - 1U;
    return (float)g_blocks[idx] / 180.0f;
}

const uint8_t *blocks_table(void)
{
    return g_blocks;
}
