#include <stdint.h>
#include <stdbool.h>
#include <string.h>

#include "driverlib/gpio.h"
#include "driverlib/pin_map.h"
#include "driverlib/ssi.h"
#include "inc/hw_ssi.h"     /* SSI_O_CR0, SSI_O_CR1, SSI_CR1_SSE */
#include "driverlib/sysctl.h"
#include "inc/hw_gpio.h"
#include "inc/hw_memmap.h"
#include "inc/hw_types.h"

#include "wizchip_conf.h"
#include "socket.h"

#include "config.h"
#include "net.h"
#include "net_persist.h"
#include "protocol.h"
#include "debug.h"

/* ── hardware pin assignments ─────────────────────────────────────────────── */

/* SSI0: CLK=PA2, MISO=PA4, MOSI=PA5 */
#define SPI_PERIPH      SYSCTL_PERIPH_SSI0
#define SPI_BASE        SSI0_BASE
#define SPI_PORT_PERIPH SYSCTL_PERIPH_GPIOA
#define SPI_PORT        GPIO_PORTA_BASE
#define PIN_CLK         GPIO_PIN_2
#define PIN_MISO        GPIO_PIN_4
#define PIN_MOSI        GPIO_PIN_5

/* CS: PA3 (software GPIO — NOT SSI0Fss, keeps CS asserted across frames) */
#define CS_PORT         GPIO_PORTA_BASE
#define CS_PIN          GPIO_PIN_3

/* RST: PD7 (NMI-locked, active low) */
#define RST_PORT_PERIPH SYSCTL_PERIPH_GPIOD
#define RST_PORT        GPIO_PORTD_BASE
#define RST_PIN         GPIO_PIN_7

/* INT: PD6 (input, polled in this step) */
#define INT_PORT        GPIO_PORTD_BASE
#define INT_PIN         GPIO_PIN_6

/* ── socket number assignments (not the same as Sn_MR_TCP/UDP types) ──────── */
#define SOCKNUM_TCP     0
#define SOCKNUM_UDP     1

/* W5500 internal buffer sizes per socket (KB, must sum ≤ 16 per direction) */
static uint8_t g_txbuf[8] = { 4, 2, 1, 1, 1, 1, 1, 1 };
static uint8_t g_rxbuf[8] = { 4, 2, 1, 1, 1, 1, 1, 1 };

/* TCP receive line buffer — holds one partial JSON line across ticks. */
#define TCP_RX_SIZE     512U
static char    g_rx_buf[TCP_RX_SIZE];
static uint16_t g_rx_idx = 0;

/* Software-tracked RX buffer pointer — bypasses getSn_RX_RD which always
   returns the initial value (0x000B) on this chip variant because Sn_RX_RD
   appears to be read-only in ESTABLISHED state.  We seed it once from the
   chip on first connection, then advance it ourselves. */
static uint16_t g_rx_rd_ptr = 0xFFFF;   /* 0xFFFF = uninitialised */

/* Software-tracked TX write pointer — bypasses getSn_TX_WR which returns
   stale/wrong values on this chip variant (same class of bug as Sn_RX_RD).
   When wiz_send_data() uses getSn_TX_WR() as its write base it computes the
   wrong buffer offset and the chip's internal send pointer (which DID advance
   correctly after the previous SEND) finds garbage between the two positions.
   We seed from getSn_TX_RD() on each new connection and advance manually. */
static uint16_t g_tx_wr_ptr = 0;

/* Brain's IP (captured from TCP peer on connect) and connection flag. */
static uint8_t  g_brain_ip[4] = {0};
static bool     g_brain_connected = false;

/* Telemetry sequence counter and tick accumulator. */
static uint32_t g_telem_seq  = 0;
static uint32_t g_telem_tick = 0;

#define TELEM_INTERVAL_TICKS  5U   /* 100 Hz / 5 = 20 Hz */

/* Forward declaration for use inside net_tick */
static void tcp_receive(const sm_ctx_t *sm);

/* Mutable network config (defaults from config.h, overridable at runtime). */
static wiz_NetInfo g_netinfo = {
    .mac  = NET_MAC,
    .ip   = NET_IP,
    .sn   = NET_SUBNET,
    .gw   = NET_GATEWAY,
    .dns  = NET_DNS,
    .dhcp = NETINFO_STATIC,
};

/* ── SPI callbacks (registered with ioLibrary) ───────────────────────────── */

static void spi_cs_assert(void)
{
    GPIOPinWrite(CS_PORT, CS_PIN, 0);
}

static void spi_cs_deassert(void)
{
    GPIOPinWrite(CS_PORT, CS_PIN, CS_PIN);
}

/* Single-byte callbacks — used by WIZCHIP_READ for the data byte after the
   burst header.  WIZCHIP_WRITE uses _write_burst for everything, so only
   _read_byte is critical, but register both for completeness. */
static uint8_t spi_readbyte(void)
{
    uint32_t data;
    SSIDataPut(SPI_BASE, 0xFF);
    SSIDataGet(SPI_BASE, &data);
    return (uint8_t)data;
}

static void spi_writebyte(uint8_t b)
{
    uint32_t dummy;
    SSIDataPut(SPI_BASE, b);
    SSIDataGet(SPI_BASE, &dummy);
}

/* Burst callbacks — used by WIZCHIP_READ/_WRITE for the 3-byte SPI header. */
static void spi_readburst(uint8_t *buf, uint16_t len)
{
    for (uint16_t i = 0; i < len; i++) {
        uint32_t data;
        SSIDataPut(SPI_BASE, 0xFF);
        SSIDataGet(SPI_BASE, &data);
        buf[i] = (uint8_t)data;
    }
}

static void spi_writeburst(uint8_t *buf, uint16_t len)
{
    for (uint16_t i = 0; i < len; i++) {
        uint32_t dummy;
        SSIDataPut(SPI_BASE, buf[i]);
        SSIDataGet(SPI_BASE, &dummy);
    }
}

/* ── hardware init ───────────────────────────────────────────────────────── */

static void spi_hw_init(void)
{
    SysCtlPeripheralEnable(SPI_PERIPH);
    SysCtlPeripheralEnable(SPI_PORT_PERIPH);
    while (!SysCtlPeripheralReady(SPI_PERIPH)) {}

    /* Alternate function for CLK, MISO, MOSI */
    GPIOPinConfigure(GPIO_PA2_SSI0CLK);
    GPIOPinConfigure(GPIO_PA4_SSI0RX);
    GPIOPinConfigure(GPIO_PA5_SSI0TX);
    GPIOPinTypeSSI(SPI_PORT, PIN_CLK | PIN_MISO | PIN_MOSI);

    /* CS as GPIO output, deasserted (high) */
    GPIOPinTypeGPIOOutput(CS_PORT, CS_PIN);
    GPIOPinWrite(CS_PORT, CS_PIN, CS_PIN);

    SSIConfigSetExpClk(SPI_BASE, SYSCLOCK_HZ,
                       SSI_FRF_MOTO_MODE_0, SSI_MODE_MASTER,
                       NET_SPI_HZ, 8);
    SSIEnable(SPI_BASE);

    /* Drain any stale data in the RX FIFO */
    uint32_t dummy;
    while (SSIDataGetNonBlocking(SPI_BASE, &dummy)) {}
}

static void rst_hw_init(void)
{
    SysCtlPeripheralEnable(RST_PORT_PERIPH);
    while (!SysCtlPeripheralReady(RST_PORT_PERIPH)) {}

    /* PD7 is the NMI pin — unlock commit register before configuring. */
    HWREG(RST_PORT + GPIO_O_LOCK) = GPIO_LOCK_KEY;
    HWREG(RST_PORT + GPIO_O_CR)  |= RST_PIN;
    HWREG(RST_PORT + GPIO_O_LOCK) = 0;

    GPIOPinTypeGPIOOutput(RST_PORT, RST_PIN);

    /* INT pin as input (polled) */
    GPIOPinTypeGPIOInput(INT_PORT, INT_PIN);

    /* Reset pulse: assert RST low for ≥ 500 ns, then wait 2 ms for W5500 PLL. */
    GPIOPinWrite(RST_PORT, RST_PIN, 0);
    SysCtlDelay(SYSCLOCK_HZ / 3 / 1000);   /* ~1 ms */
    GPIOPinWrite(RST_PORT, RST_PIN, RST_PIN);
    SysCtlDelay(SYSCLOCK_HZ / 3 / 500);    /* ~2 ms */
}

/* ── RX buffer byte read in 9-bit SSI mode ───────────────────────────────
 * This chip variant inserts one dummy 0-bit before each data byte when
 * reading from the socket RX buffer block (BSB=3).  In 8-bit SSI we only
 * capture bits [1..8] → data >> 1.  Switching to 9-bit SSI captures all
 * 9 bits; the lower 8 are the correct data byte.
 *
 * The SSI is disabled, reconfigured (8-bit → 9-bit), re-enabled, and
 * restored to 8-bit after the read — all while CS stays asserted, so the
 * W5500 treats it as one continuous transaction. */
static uint8_t rxbuf_read_9bit(uint16_t ptr)
{
    uint32_t data;

    uint32_t addrsel = ((uint32_t)ptr << 8)
                       + (uint32_t)(WIZCHIP_RXBUF_BLOCK(SOCKNUM_TCP) << 3)
                       + 0x00U;  /* RWB=0 (read), OM=00 (VDM) */
    uint8_t hdr[3] = {
        (uint8_t)((addrsel >> 16) & 0xFFU),
        (uint8_t)((addrsel >>  8) & 0xFFU),
        (uint8_t)((addrsel >>  0) & 0xFFU),
    };

    spi_cs_assert();
    spi_writeburst(hdr, 3);

    uint32_t cr0 = HWREG(SPI_BASE + SSI_O_CR0);
    HWREG(SPI_BASE + SSI_O_CR1) &= ~SSI_CR1_SSE;
    HWREG(SPI_BASE + SSI_O_CR0)  = (cr0 & ~0x0FUL) | 0x08UL;  /* 9-bit */
    HWREG(SPI_BASE + SSI_O_CR1) |=  SSI_CR1_SSE;
    while (SSIDataGetNonBlocking(SPI_BASE, &data)) {}

    SSIDataPut(SPI_BASE, 0x1FFUL);
    SSIDataGet(SPI_BASE, &data);
    uint8_t result = (uint8_t)(data & 0xFFU);

    HWREG(SPI_BASE + SSI_O_CR1) &= ~SSI_CR1_SSE;
    HWREG(SPI_BASE + SSI_O_CR0)  = (cr0 & ~0x0FUL) | 0x07UL;  /* 8-bit */
    HWREG(SPI_BASE + SSI_O_CR1) |=  SSI_CR1_SSE;

    spi_cs_deassert();
    return result;
}

/* ── public API ──────────────────────────────────────────────────────────── */

bool net_is_connected(void) { return g_brain_connected; }

void net_init(void)
{
    /* Load EEPROM config override; falls back to config.h factory defaults. */
    net_persist_init();
    net_persist_load(&g_netinfo);   /* no-op and returns false if EEPROM blank */

    spi_hw_init();
    rst_hw_init();

    reg_wizchip_cs_cbfunc(spi_cs_assert, spi_cs_deassert);
    /* Must register single-byte callbacks: WIZCHIP_READ always calls
       _read_byte for the data byte even when burst mode is used for headers. */
    reg_wizchip_spi_cbfunc(spi_readbyte, spi_writebyte);
    reg_wizchip_spiburst_cbfunc(spi_readburst, spi_writeburst);

    /* ── Normal init ────────────────────────────────────────────────────── */
    int8_t rc = wizchip_init(g_txbuf, g_rxbuf);
    debug_log("wizchip_init rc=%d\r\n", rc);
    debug_log("Sn_SR (lib): 0x%02X  expect 0x00\r\n", getSn_SR(0));

    wizchip_setnetinfo(&g_netinfo);

    int8_t soc_rc = socket(0, Sn_MR_TCP, NET_TCP_PORT, 0);
    setSn_TX_WR(0, getSn_TX_RD(0));    /* flush TX before listen */
    int8_t lst_rc = listen(0);
    debug_log("socket()=%d listen()=%d  Sn_SR=0x%02X\r\n",
              soc_rc, lst_rc, getSn_SR(0));
}

void net_set_config(const net_config_t *cfg)
{
    memcpy(g_netinfo.mac, cfg->mac,     6);
    memcpy(g_netinfo.ip,  cfg->ip,      4);
    memcpy(g_netinfo.sn,  cfg->subnet,  4);
    memcpy(g_netinfo.gw,  cfg->gateway, 4);
    wizchip_setnetinfo(&g_netinfo);
    net_persist_save(&g_netinfo);

    /* Reset TCP socket so it re-opens and listens on the new IP. */
    g_brain_connected = false;
    g_rx_idx          = 0;
    g_rx_rd_ptr       = 0xFFFF;
    disconnect(SOCKNUM_TCP);
}

/* ── TCP receive: buffer bytes, parse complete lines ──────────────────────── */

static void tcp_receive(const sm_ctx_t *sm)
{
    int32_t avail = getSn_RX_RSR(SOCKNUM_TCP);

    bool skip_normal_read = false;

    if (avail <= 0) {
        if (g_rx_idx > 0 && g_rx_rd_ptr != 0xFFFF &&
            g_rx_idx < TCP_RX_SIZE - 1u) {
            /* RSR returns 0 for the last byte(s) on this chip variant.
               Try a speculative single-byte read. */
            uint8_t spec = rxbuf_read_9bit(g_rx_rd_ptr);
            if (spec == '\n' || spec == '\r') {
                /* Genuine terminator — append and parse. */
                g_rx_buf[g_rx_idx++] = (char)spec;
                g_rx_rd_ptr++;
                setSn_RX_RD(SOCKNUM_TCP, g_rx_rd_ptr);
                setSn_CR(SOCKNUM_TCP, Sn_CR_RECV);
                while (getSn_CR(SOCKNUM_TCP)) {}
                skip_normal_read = true;
            } else if (g_rx_buf[g_rx_idx - 1] == '}') {
                g_rx_buf[g_rx_idx++] = '\n';
                skip_normal_read = true;
            } else {
                return;   /* genuinely incomplete — wait for more data */
            }
        } else {
            return;
        }
    }

    if (!skip_normal_read) {
        /* Seed software pointer from chip on first use after connection. */
        if (g_rx_rd_ptr == 0xFFFF) {
            g_rx_rd_ptr = getSn_RX_RD(SOCKNUM_TCP);
            debug_log("RX ptr seed 0x%04X\r\n", g_rx_rd_ptr);
        }

        uint16_t space = (uint16_t)(TCP_RX_SIZE - g_rx_idx - 1u);
        if (space == 0) { g_rx_idx = 0; return; }
        uint16_t want = (avail < space) ? (uint16_t)avail : space;

        /* BSB=3 (RX buffer) reads are 1-bit right-shifted on this chip variant.
           rxbuf_read_9bit() switches SSI to 9-bit mode to capture the extra
           dummy bit, then restores 8-bit mode. */
        for (uint16_t i = 0; i < want; i++) {
            ((uint8_t *)g_rx_buf + g_rx_idx)[i] =
                rxbuf_read_9bit((uint16_t)(g_rx_rd_ptr + i));
        }
        g_rx_rd_ptr += want;

        setSn_RX_RD(SOCKNUM_TCP, g_rx_rd_ptr);
        setSn_CR(SOCKNUM_TCP, Sn_CR_RECV);
        while (getSn_CR(SOCKNUM_TCP)) {}

        g_rx_idx = (uint16_t)(g_rx_idx + want);
    }

    char *line = g_rx_buf;
    char *nl;
    while ((nl = (char *)memchr(line, '\n', (size_t)(g_rx_buf + g_rx_idx - line)))) {
        debug_log("found NL at off=%d\r\n", (int)(nl - g_rx_buf));
        if (nl > line && *(nl - 1) == '\r') { *(nl - 1) = '\0'; }
        *nl = '\0';

        uint32_t seq = 0;
        sm_command_t cmd;
        bool ok = protocol_parse(line, &seq, &cmd);

        /* Network-layer commands are handled here (not through state machine). */
        bool netcfg_pending = false;
        bool netcfg_reset   = false;
        if (ok) {
            if (cmd.type == CMD_TYPE_SET_NETCONFIG) {
                netcfg_pending = true;   /* apply AFTER ack is sent */
            } else if (cmd.type == CMD_TYPE_RESET_NETCONFIG) {
                netcfg_reset = true;
            } else {
                sm_push_command((sm_ctx_t *)sm, &cmd);
            }
            debug_log("RX seq=%u type=%u\r\n", (unsigned)seq, (unsigned)cmd.type);
        } else {
            debug_log("RX parse err: %s\r\n", line);
        }

        const char *ack = protocol_encode_ack(seq, ok, ok ? NULL : "parse error");
        /* Write ack using our software TX pointer instead of wiz_send_data().
           getSn_TX_WR() returns wrong values on this chip variant causing
           wiz_send_data() to write at the wrong buffer offset; the chip's
           internal send pointer (correctly positioned from the previous SEND)
           then sweeps backward through garbage before reaching our ack. */
        {
            uint16_t ack_len = (uint16_t)strlen(ack);
            uint32_t addrsel = ((uint32_t)g_tx_wr_ptr << 8)
                               | (uint32_t)(WIZCHIP_TXBUF_BLOCK(SOCKNUM_TCP) << 3);
            WIZCHIP_WRITE_BUF(addrsel, (uint8_t *)ack, ack_len);
            g_tx_wr_ptr += ack_len;
            setSn_TX_WR(SOCKNUM_TCP, g_tx_wr_ptr);
            debug_log("TX ack seq=%u len=%u ptr→0x%04X\r\n",
                      (unsigned)seq, (unsigned)ack_len, (unsigned)g_tx_wr_ptr);
            setSn_CR(SOCKNUM_TCP, Sn_CR_SEND);
            for (uint32_t _i = 0; _i < 5000UL && getSn_CR(SOCKNUM_TCP); _i++) {}
        }

        /* Apply network config changes AFTER ack is sent so the brain receives
           confirmation before the TCP connection is torn down by the IP change. */
        if (netcfg_pending) {
            net_config_t nc;
            memcpy(nc.ip,      cmd.netconfig.ip,      4);
            memcpy(nc.subnet,  cmd.netconfig.subnet,  4);
            memcpy(nc.gateway, cmd.netconfig.gateway, 4);
            if (cmd.netconfig.has_mac) {
                memcpy(nc.mac, cmd.netconfig.mac, 6);
            } else {
                memcpy(nc.mac, g_netinfo.mac, 6);
            }
            net_set_config(&nc);   /* saves to EEPROM + resets TCP socket */
            return;                /* tcp_receive returns; net_tick will reopen */
        }
        if (netcfg_reset) {
            net_persist_clear();   /* next boot uses config.h factory defaults */
        }
        line = nl + 1;
    }

    uint16_t remaining = (uint16_t)(g_rx_buf + g_rx_idx - line);
    if (remaining > 0 && line != g_rx_buf) { memmove(g_rx_buf, line, remaining); }
    g_rx_idx = remaining;
    if (g_rx_idx >= TCP_RX_SIZE - 1u) { g_rx_idx = 0; }
}

/* ── net_tick ─────────────────────────────────────────────────────────────── */

void net_tick(const sm_ctx_t *sm, float az_raw, float el_raw, uint32_t ts_ms)
{
    /* TCP socket lifecycle */
    uint8_t tcp_sr = getSn_SR(SOCKNUM_TCP);
    switch (tcp_sr) {
    case SOCK_CLOSED:
        g_brain_connected = false;
        g_rx_idx = 0;
        g_rx_rd_ptr = 0xFFFF;
        socket(SOCKNUM_TCP, Sn_MR_TCP, NET_TCP_PORT, 0);
        /* Do NOT call listen() here — W5500 needs one tick to settle from
           the transitional state into SOCK_INIT.  The SOCK_INIT case below
           calls listen() once Sn_SR is stable. */
        break;
    case SOCK_INIT:
        /* The TX pointer sync (0-byte SEND) moved to SOCK_ESTABLISHED so it
           runs after a real TCP connection exists.  Just call listen() here. */
        listen(SOCKNUM_TCP);
        break;
    case SOCK_LISTEN:
        break;
    case SOCK_ESTABLISHED:
        if (!g_brain_connected) {
            getSn_DIPR(SOCKNUM_TCP, g_brain_ip);
            g_brain_connected = true;

            /* Seed software TX pointer from chip TX_RD, then issue a 0-byte
               SEND so the chip's internal send pointer advances to match.
               Without this, any desync between the chip's internal pointer and
               the register produces garbage bytes before the first real ack. */
            g_tx_wr_ptr = getSn_TX_RD(SOCKNUM_TCP);
            setSn_TX_WR(SOCKNUM_TCP, g_tx_wr_ptr);
            setSn_CR(SOCKNUM_TCP, Sn_CR_SEND);
            for (uint32_t _i = 0; _i < 5000UL && getSn_CR(SOCKNUM_TCP); _i++) {}

            debug_log("brain %u.%u.%u.%u TX_RD=0x%04X\r\n",
                      g_brain_ip[0], g_brain_ip[1],
                      g_brain_ip[2], g_brain_ip[3],
                      (unsigned)g_tx_wr_ptr);
        }
        tcp_receive(sm);
        break;
    case SOCK_CLOSE_WAIT:
        g_brain_connected = false;
        g_rx_idx = 0;
        g_rx_rd_ptr = 0xFFFF;   /* reseed on next connection */
        disconnect(SOCKNUM_TCP);
        break;
    default:
        break;
    }

    /* Telemetry blast at 20 Hz over TCP.
       The W5500 on this module silently drops UDP SEND commands (Sn_MR is
       cleared after OPEN so the chip's internal mode check fails).  TCP SEND
       on socket 0 works correctly via the same g_tx_wr_ptr bypass used for
       acks.  The brain routes frames by "type" field. */
    if (++g_telem_tick >= TELEM_INTERVAL_TICKS) {
        g_telem_tick = 0;
        if (g_brain_connected) {
            const char *frame = protocol_encode_telemetry(
                sm, az_raw, el_raw, ts_ms, g_telem_seq++);
            uint16_t frame_len = (uint16_t)strlen(frame);
            uint32_t addrsel = ((uint32_t)g_tx_wr_ptr << 8)
                               | (uint32_t)(WIZCHIP_TXBUF_BLOCK(SOCKNUM_TCP) << 3);
            WIZCHIP_WRITE_BUF(addrsel, (uint8_t *)frame, frame_len);
            g_tx_wr_ptr += frame_len;
            setSn_TX_WR(SOCKNUM_TCP, g_tx_wr_ptr);
            setSn_CR(SOCKNUM_TCP, Sn_CR_SEND);
            for (uint32_t _i = 0; _i < 5000UL && getSn_CR(SOCKNUM_TCP); _i++) {}
        }
    }
}
