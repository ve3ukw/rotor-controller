#include <stdint.h>
#include <stdbool.h>
#include <string.h>

#include "driverlib/gpio.h"
#include "driverlib/i2c.h"
#include "driverlib/pin_map.h"
#include "driverlib/sysctl.h"
#include "inc/hw_memmap.h"

#include "config.h"
#include "debug.h"
#include "display.h"

/* PCF8574 bit assignments come from config.h (PCF_RS / PCF_RW / PCF_EN / PCF_BL).
   DB4..DB7 map to bits 4..7 on the PCF8574 regardless of variant. */

/* ── HD44780 row start addresses ─────────────────────────────────────────── */
/* All four rows accessible now that brev4() corrects the reversed data bus.
   Standard 4×20 HD44780 DDRAM layout. */
static const uint8_t k_row_addr[4] = { 0x00, 0x40, 0x14, 0x54 };

/* ── module state ────────────────────────────────────────────────────────── */
static bool     g_present  = false;
static uint8_t  g_lcd_addr = LCD_I2C_ADDR;  /* overwritten by scan */
static uint32_t g_tick_ctr = 0;

/* ── low-level I2C write ─────────────────────────────────────────────────── */
static bool i2c_send(uint8_t val)
{
    I2CMasterSlaveAddrSet(I2C1_BASE, g_lcd_addr, false);
    I2CMasterDataPut(I2C1_BASE, val);
    I2CMasterControl(I2C1_BASE, I2C_MASTER_CMD_SINGLE_SEND);
    while (I2CMasterBusy(I2C1_BASE)) {}
    return (I2CMasterErr(I2C1_BASE) == I2C_MASTER_ERR_NONE);
}

/* Probe one address; returns true if something ACKs there. */
static bool i2c_probe(uint8_t addr)
{
    I2CMasterSlaveAddrSet(I2C1_BASE, addr, false);
    I2CMasterDataPut(I2C1_BASE, PCF_BL);
    I2CMasterControl(I2C1_BASE, I2C_MASTER_CMD_SINGLE_SEND);
    while (I2CMasterBusy(I2C1_BASE)) {}
    return (I2CMasterErr(I2C1_BASE) == I2C_MASTER_ERR_NONE);
}

/* Scan the full PCF8574 / PCF8574A address space and return first hit.
   Most common first: 0x27 (PCF8574, A2/A1/A0 = 1), 0x3F (PCF8574A). */
static uint8_t i2c_scan(void)
{
    static const uint8_t candidates[] = {
        0x27, 0x3F,                          /* most common */
        0x26, 0x25, 0x24, 0x23, 0x22, 0x21, 0x20,   /* PCF8574 */
        0x3E, 0x3D, 0x3C, 0x3B, 0x3A, 0x39, 0x38,   /* PCF8574A */
    };
    for (uint8_t i = 0; i < sizeof(candidates); i++) {
        if (i2c_probe(candidates[i])) {
            return candidates[i];
        }
    }
    return 0;
}

/* ── HD44780 via PCF8574 ─────────────────────────────────────────────────── */
static void lcd_en_pulse(uint8_t val)
{
    i2c_send(val | PCF_EN);
    SysCtlDelay(SYSCLOCK_HZ / 3 / 200000);   /* ~5 µs high */
    i2c_send(val & ~PCF_EN);
    SysCtlDelay(SYSCLOCK_HZ / 3 / 100000);   /* ~10 µs low — give clone time to latch */
}

static void lcd_nibble(uint8_t nibble, uint8_t rs)
{
#ifdef LCD_NIBBLE_REVERSED
    /* Reverse 4 bits: for modules wired P4=DB7, P5=DB6, P6=DB5, P7=DB4. */
    uint8_t n = nibble & 0x0Fu;
    uint8_t rev = (uint8_t)(((n&1u)<<3)|((n&2u)<<1)|((n&4u)>>1)|((n&8u)>>3));
    uint8_t v = (uint8_t)((rev << 4) | PCF_BL | (rs ? PCF_RS : 0u));
#else
    uint8_t v = (uint8_t)(((nibble & 0x0Fu) << 4) | PCF_BL | (rs ? PCF_RS : 0u));
#endif
    lcd_en_pulse(v);
}

static void lcd_byte(uint8_t data, uint8_t rs)
{
    lcd_nibble(data >> 4,   rs);
    lcd_nibble(data & 0x0F, rs);
}

static void lcd_cmd(uint8_t cmd)  { lcd_byte(cmd, 0); }
static void lcd_data(uint8_t ch)  { lcd_byte(ch,  1); }

static void lcd_cursor(uint8_t row, uint8_t col)
{
    lcd_cmd(0x80 | (k_row_addr[row] + col));
}

static void lcd_puts(const char *s)
{
    while (*s) { lcd_data((uint8_t)*s++); }
}

/* ── formatting helpers (no floating-point printf needed) ────────────────── */

/* Format 0.0–1.0 float as "0.XXX" (5 chars, null-terminated). */
static void fmt_norm(char *buf, float v)
{
    if (v < 0.0f) { v = 0.0f; }
    if (v > 1.0f) { v = 1.0f; }
    uint32_t thou = (uint32_t)(v * 1000.0f + 0.5f);
    buf[0] = '0';
    buf[1] = '.';
    buf[2] = (char)('0' + thou / 100);
    buf[3] = (char)('0' + (thou / 10) % 10);
    buf[4] = (char)('0' + thou % 10);
    buf[5] = '\0';
}

/* Write exactly `width` characters (space-padded) from `s`. */
static void lcd_field(const char *s, uint8_t width)
{
    uint8_t i = 0;
    while (s[i] && i < width) { lcd_data((uint8_t)s[i++]); }
    while (i++ < width)        { lcd_data(' '); }
}

/* ── row renderers ───────────────────────────────────────────────────────── */


/* ── public API ──────────────────────────────────────────────────────────── */

void display_init(void)
{
    SysCtlPeripheralEnable(SYSCTL_PERIPH_I2C1);
    SysCtlPeripheralEnable(SYSCTL_PERIPH_GPIOA);
    while (!SysCtlPeripheralReady(SYSCTL_PERIPH_I2C1)) {}

    GPIOPinConfigure(GPIO_PA6_I2C1SCL);
    GPIOPinConfigure(GPIO_PA7_I2C1SDA);
    GPIOPinTypeI2CSCL(GPIO_PORTA_BASE, GPIO_PIN_6);
    GPIOPinTypeI2C(GPIO_PORTA_BASE, GPIO_PIN_7);

    I2CMasterInitExpClk(I2C1_BASE, SYSCLOCK_HZ, false);  /* 100 kHz — safer for cheap modules */

    /* Scan FIRST so the init sequence goes to the correct address.
       Previously the scan ran after the init sequence, so the three reset
       pulses and 4-bit mode switch were sent to the default 0x27 (NAK'd),
       leaving the LCD in 8-bit power-on-reset mode → garbled nibble output. */
    uint8_t addr = i2c_scan();
    if (addr == 0) {
        debug_log("LCD: not found\r\n");
        return;
    }
    g_lcd_addr = addr;
    debug_log("LCD: found at 0x%02X\r\n", addr);

    /* HD44780 power-on sequence (≥50 ms after Vcc rise) */
    SysCtlDelay(SYSCLOCK_HZ / 3 / 20);    /* 50 ms */

    /* Three 8-bit function-set pulses to reset */
    lcd_nibble(0x03, 0); SysCtlDelay(SYSCLOCK_HZ / 3 / 250);   /* 4 ms */
    lcd_nibble(0x03, 0); SysCtlDelay(SYSCLOCK_HZ / 3 / 10000); /* 100 µs */
    lcd_nibble(0x03, 0); SysCtlDelay(SYSCLOCK_HZ / 3 / 10000);

    /* Switch to 4-bit mode */
    lcd_nibble(0x02, 0);

    /* Configuration */
    lcd_cmd(0x28);   /* 4-bit, 2-line, 5×8 */
    lcd_cmd(0x08);   /* display off */
    lcd_cmd(0x01);   /* clear */
    SysCtlDelay(SYSCLOCK_HZ / 3 / 700);  /* 2 ms for clear */
    lcd_cmd(0x06);   /* entry: increment, no shift */
    lcd_cmd(0x0C);   /* display on, cursor off, blink off */

    g_present = true;

    /* Blank all rows on boot — splash or live content follows. */
    for (uint8_t r = 0; r < 4; r++) {
        lcd_cursor(r, 0);
        for (uint8_t c = 0; c < LCD_COLS; c++) { lcd_data(' '); }
    }
}

void display_splash(const char *row0, const char *row1,
                    const char *row2, const char *row3)
{
    if (!g_present) { return; }
    const char *rows[4] = { row0, row1, row2, row3 };
    for (uint8_t r = 0; r < 4; r++) {
        lcd_cursor(r, 0);
        lcd_field(rows[r] ? rows[r] : "", LCD_COLS);
    }
}

/* ── 4-row renders ───────────────────────────────────────────────────────── */

static void render_az(const sm_ctx_t *sm, float az)
{
    lcd_cursor(0, 0);
    if (sm_get_state(sm) == SM_STATE_FAULT_ADC_INVALID) {
        lcd_puts("AZ: NO SENSOR       ");
        return;
    }
    /* "AZ:0.400  [>> CW  ]" — 20 chars */
    char pos[6]; fmt_norm(pos, az);
    const char *motion;
    if ((sm->estop_active || sm->estop_hw_latch) && sm_get_az_motion(sm) == SM_AZ_STOP) {
        motion = "[E-STOP!]";
    } else {
        switch (sm_get_az_motion(sm)) {
        case SM_AZ_CW:  motion = "[>> CW  ]"; break;
        case SM_AZ_CCW: motion = "[<< CCW ]"; break;
        default:        motion = "[  STOP ]"; break;
        }
    }
    lcd_puts("AZ:"); lcd_puts(pos); lcd_puts("  "); lcd_puts(motion); lcd_data(' ');
}

static void render_el(const sm_ctx_t *sm, float el)
{
    lcd_cursor(1, 0);
    if (sm_get_state(sm) == SM_STATE_FAULT_ADC_INVALID) {
        lcd_puts("EL: NO SENSOR       ");
        return;
    }
    /* "EL:0.250  [^^ UP  ]" — 20 chars */
    char pos[6]; fmt_norm(pos, el);
    const char *motion;
    if ((sm->estop_active || sm->estop_hw_latch) && sm_get_el_motion(sm) == SM_EL_STOP) {
        motion = "[E-STOP!]";
    } else {
        switch (sm_get_el_motion(sm)) {
        case SM_EL_UP:   motion = "[^^ UP  ]"; break;
        case SM_EL_DOWN: motion = "[vv DOWN]"; break;
        default:         motion = "[  STOP ]"; break;
        }
    }
    lcd_puts("EL:"); lcd_puts(pos); lcd_puts("  "); lcd_puts(motion); lcd_data(' ');
}

static void render_state(const sm_ctx_t *sm)
{
    /* "IDLE        LINKED  " — 12 + 8 = 20 chars.
       Use short names that fit cleanly in 12 chars.              */
    const char *st12;
    switch (sm_get_state(sm)) {
    case SM_STATE_IDLE:              st12 = "IDLE        "; break;
    case SM_STATE_MOVING:            st12 = "MOVING      "; break;
    case SM_STATE_PARKING:           st12 = "PARKING     "; break;
    case SM_STATE_FAULT_LINK_LOST:   st12 = "FAULT:LINK  "; break;
    case SM_STATE_FAULT_LIMIT:       st12 = "FAULT:LIMIT "; break;
    case SM_STATE_FAULT_DUTY_CYCLE:  st12 = "FAULT:DUTY  "; break;
    case SM_STATE_FAULT_ADC_INVALID: st12 = "NO G-5500   "; break;
    default:                         st12 = "???         "; break;
    }
    lcd_cursor(2, 0);
    lcd_puts(st12);
    lcd_field(sm->brain_ever_connected ? "LINKED  " : "NO LINK ", 8);
}

static void render_status(const sm_ctx_t *sm)
{
    /* "A:00% E:00%  V-U-L-R" — 20 chars */
    uint8_t az_d = sm_duty_az_pct(sm);
    uint8_t el_d = sm_duty_el_pct(sm);
    lcd_cursor(3, 0);
    lcd_puts("A:");
    lcd_data((char)('0' + az_d / 10)); lcd_data((char)('0' + az_d % 10)); lcd_data('%');
    lcd_puts(" E:");
    lcd_data((char)('0' + el_d / 10)); lcd_data((char)('0' + el_d % 10)); lcd_data('%');
    lcd_puts("  ");
    lcd_data(sm->pol_vhf  ? 'V' : '-');
    lcd_data(sm->pol_uhf  ? 'U' : '-');
    lcd_data(sm->lna_uhf  ? 'L' : '-');
    lcd_data(sm->rxtx_uhf ? 'R' : '-');
}

void display_tick(const sm_ctx_t *sm, float az_raw, float el_raw)
{
    if (!g_present) { return; }

    /* Write all 4 rows in one burst every 100 ticks (1 s).
       A single burst is visually cleaner than 4 staggered writes
       each causing a separate 1 Hz blink on different rows. */
    g_tick_ctr++;
    if (g_tick_ctr < 500u) { return; }                    /* 5 s splash hold */
    if ((g_tick_ctr - 500u) % 100u != 0u) { return; }     /* 1 Hz thereafter */

    render_az(sm, az_raw);
    render_el(sm, el_raw);
    render_state(sm);
    render_status(sm);
}
