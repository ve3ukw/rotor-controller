#include <stdint.h>
#include <stdbool.h>
#include <string.h>

#include "driverlib/adc.h"
#include "driverlib/gpio.h"
#include "driverlib/interrupt.h"
#include "driverlib/sysctl.h"
#include "driverlib/timer.h"
#include "driverlib/udma.h"
#include "inc/hw_adc.h"
#include "inc/hw_ints.h"
#include "inc/hw_memmap.h"
#include "inc/hw_types.h"

#include "config.h"
#include "adc.h"

/* ── hardware assignments ─────────────────────────────────────────────────── */
#define ADC_AZ_CHANNEL      ADC_CTL_CH4     /* AIN4 = PD3 */
#define ADC_EL_CHANNEL      ADC_CTL_CH2     /* AIN2 = PE1 */
#define ADC_PERIPH_AZ_PORT  SYSCTL_PERIPH_GPIOD
#define ADC_PERIPH_EL_PORT  SYSCTL_PERIPH_GPIOE
#define ADC_AZ_PORT         GPIO_PORTD_BASE
#define ADC_EL_PORT         GPIO_PORTE_BASE
#define ADC_AZ_PIN          GPIO_PIN_3
#define ADC_EL_PIN          GPIO_PIN_1

/* ── sampling parameters ──────────────────────────────────────────────────── */
#define ADC_SAMPLE_RATE_HZ  1000U
#define ADC_TIMER_RELOAD    ((SYSCLOCK_HZ / ADC_SAMPLE_RATE_HZ) - 1U)

/* DMA ping-pong: each buffer holds BATCH_SIZE triggers × 2 channels.
   The ISR fires every BATCH_SIZE ms (= 16 ms at 1 kHz). */
#define ADC_BATCH_SIZE      16U
#define ADC_DMA_BUF_WORDS   (ADC_BATCH_SIZE * 2U)  /* az, el, az, el … */

/* Median filter window and ring buffer (ring must be ≥ FILTER_N, power-of-2). */
#define ADC_FILTER_N        16U
#define ADC_RING_SIZE       32U

/* Plausibility: max normalized change per main-loop tick (10 ms).
   G-5500 max slew ≈ 11°/s az, 4.5°/s el.  Use 50× margin to catch only
   genuine RF-spike induced jumps, not legitimate fast motion. */
#define ADC_MAX_DELTA       0.05f

/* Fault if this many rejects occur within a 1-second window. */
#define ADC_NOISE_THRESHOLD     10U
#define ADC_NOISE_WINDOW_TICKS  TICK_HZ

/* ── DMA control table (1024-byte aligned, in SRAM) ──────────────────────── */
static uint8_t  g_udma_ctrl[1024] __attribute__((aligned(1024)));

/* Ping-pong destination buffers.  SS0 step order: AIN4 (az) then AIN2 (el). */
static uint32_t g_dma_buf[2][ADC_DMA_BUF_WORDS];

/* Per-channel ring buffers (ISR writes, main loop reads). */
static volatile uint32_t g_az_ring[ADC_RING_SIZE];
static volatile uint32_t g_el_ring[ADC_RING_SIZE];
static volatile uint32_t g_ring_head = 0;   /* next write slot */

/* ── plausibility state ───────────────────────────────────────────────────── */
static float    g_az_filtered  = 0.0f;
static float    g_el_filtered  = 0.0f;
static uint32_t g_az_rejects   = 0;
static uint32_t g_el_rejects   = 0;
static uint32_t g_window_tick  = 0;   /* tick counter for noise window */
static bool     g_fault        = false;
static bool     g_primed       = false;  /* ring has ≥ FILTER_N samples */
/* Seed flags: first read after priming accepts the actual antenna position
   unconditionally so the filter doesn't start from 0.0 and immediately
   reject any antenna that isn't at its minimum travel position. */
static bool     g_az_seeded    = false;
static bool     g_el_seeded    = false;

/* ── median filter ────────────────────────────────────────────────────────── */
static float median_normalized(const volatile uint32_t *ring, uint32_t head)
{
    uint32_t buf[ADC_FILTER_N];
    for (uint32_t i = 0; i < ADC_FILTER_N; i++) {
        uint32_t idx = (head - ADC_FILTER_N + i) & (ADC_RING_SIZE - 1U);
        buf[i] = ring[idx];
    }
    /* Insertion sort — efficient for small N. */
    for (uint32_t i = 1; i < ADC_FILTER_N; i++) {
        uint32_t key = buf[i];
        int32_t  j   = (int32_t)i - 1;
        while (j >= 0 && buf[j] > key) {
            buf[j + 1] = buf[j];
            j--;
        }
        buf[j + 1] = key;
    }
    /* Upper-middle element of even-length sorted array. */
    return (float)buf[ADC_FILTER_N / 2U] / 4095.0f;
}

/* ── ADC0 SS0 interrupt (DMA ping-pong completion) ───────────────────────── */
void ADC0SS0_Handler(void)
{
    ADCIntClear(ADC0_BASE, 0);

    /* Determine which ping-pong buffer just completed (mode → STOP). */
    bool pri_done =
        (uDMAChannelModeGet(UDMA_CHANNEL_ADC0 | UDMA_PRI_SELECT) == UDMA_MODE_STOP);
    const uint32_t *src = pri_done ? g_dma_buf[0] : g_dma_buf[1];

    /* Re-arm the completed descriptor immediately so DMA does not stall. */
    uint32_t sel = pri_done ? UDMA_PRI_SELECT : UDMA_ALT_SELECT;
    uDMAChannelTransferSet(
        UDMA_CHANNEL_ADC0 | sel,
        UDMA_MODE_PINGPONG,
        (void *)(ADC0_BASE + ADC_O_SSFIFO0),
        pri_done ? g_dma_buf[0] : g_dma_buf[1],
        ADC_DMA_BUF_WORDS);

    /* Push samples into ring buffers.  ISR rule: short — copy and return. */
    uint32_t head = g_ring_head;
    for (uint32_t i = 0; i < ADC_BATCH_SIZE; i++) {
        uint32_t slot = (head + i) & (ADC_RING_SIZE - 1U);
        g_az_ring[slot] = src[i * 2U];
        g_el_ring[slot] = src[i * 2U + 1U];
    }
    g_ring_head = (head + ADC_BATCH_SIZE) & (ADC_RING_SIZE - 1U);
}

/* ── public API ───────────────────────────────────────────────────────────── */

void adc_init(void)
{
    /* GPIO for analog inputs — no pull, no drive, analog function. */
    SysCtlPeripheralEnable(ADC_PERIPH_AZ_PORT);
    SysCtlPeripheralEnable(ADC_PERIPH_EL_PORT);
    SysCtlPeripheralEnable(SYSCTL_PERIPH_ADC0);
    SysCtlPeripheralEnable(SYSCTL_PERIPH_TIMER1);
    SysCtlPeripheralEnable(SYSCTL_PERIPH_UDMA);
    while (!SysCtlPeripheralReady(SYSCTL_PERIPH_ADC0)) {}

    GPIOPinTypeADC(ADC_AZ_PORT, ADC_AZ_PIN);
    GPIOPinTypeADC(ADC_EL_PORT, ADC_EL_PIN);

    /* ADC0 SS0: triggered by Timer1A, priority 0, 2-step sequence.
       Step 0: AIN4 (az).  Step 1: AIN2 (el), end of sequence + DMA/int. */
    ADCSequenceConfigure(ADC0_BASE, 0, ADC_TRIGGER_TIMER, 0);
    ADCSequenceStepConfigure(ADC0_BASE, 0, 0, ADC_AZ_CHANNEL);
    ADCSequenceStepConfigure(ADC0_BASE, 0, 1,
        ADC_EL_CHANNEL | ADC_CTL_END | ADC_CTL_IE);

    /* uDMA: control table, then channel 14 = ADC0 SS0. */
    uDMAEnable();
    uDMAControlBaseSet(g_udma_ctrl);

    uDMAChannelAssign(UDMA_CH14_ADC0_0);
    uDMAChannelAttributeDisable(UDMA_CHANNEL_ADC0, UDMA_ATTR_ALL);

    /* Primary descriptor. */
    uDMAChannelControlSet(UDMA_CHANNEL_ADC0 | UDMA_PRI_SELECT,
        UDMA_SIZE_32 | UDMA_SRC_INC_NONE | UDMA_DST_INC_32 | UDMA_ARB_2);
    uDMAChannelTransferSet(UDMA_CHANNEL_ADC0 | UDMA_PRI_SELECT,
        UDMA_MODE_PINGPONG,
        (void *)(ADC0_BASE + ADC_O_SSFIFO0),
        g_dma_buf[0],
        ADC_DMA_BUF_WORDS);

    /* Alternate descriptor. */
    uDMAChannelControlSet(UDMA_CHANNEL_ADC0 | UDMA_ALT_SELECT,
        UDMA_SIZE_32 | UDMA_SRC_INC_NONE | UDMA_DST_INC_32 | UDMA_ARB_2);
    uDMAChannelTransferSet(UDMA_CHANNEL_ADC0 | UDMA_ALT_SELECT,
        UDMA_MODE_PINGPONG,
        (void *)(ADC0_BASE + ADC_O_SSFIFO0),
        g_dma_buf[1],
        ADC_DMA_BUF_WORDS);

    uDMAChannelEnable(UDMA_CHANNEL_ADC0);

    /* Route ADC DMA request and enable SS0 interrupt. */
    ADCSequenceDMAEnable(ADC0_BASE, 0);
    ADCIntEnable(ADC0_BASE, 0);
    IntEnable(INT_ADC0SS0);

    /* Timer1A: periodic, fires at 1 kHz, output triggers ADC. */
    TimerConfigure(TIMER1_BASE, TIMER_CFG_SPLIT_PAIR | TIMER_CFG_A_PERIODIC);
    TimerLoadSet(TIMER1_BASE, TIMER_A, (uint32_t)ADC_TIMER_RELOAD);
    TimerControlTrigger(TIMER1_BASE, TIMER_A, true);
    TimerEnable(TIMER1_BASE, TIMER_A);

    ADCSequenceEnable(ADC0_BASE, 0);
}

void adc_tick(void)
{
    /* Mark ring primed once we have enough samples for the filter. */
    if (!g_primed && g_ring_head >= ADC_FILTER_N) {
        g_primed = true;
    }

    /* Slide the noise window every NOISE_WINDOW_TICKS ticks. */
    if (++g_window_tick >= ADC_NOISE_WINDOW_TICKS) {
        g_window_tick = 0;
        g_az_rejects  = 0;
        g_el_rejects  = 0;
    }
}

float adc_get_az(void)
{
    if (!g_primed) { return g_az_filtered; }

    uint32_t head = g_ring_head;  /* atomic 32-bit read */
    float v = median_normalized(g_az_ring, head);

    if (!g_az_seeded) {
        g_az_filtered = v;   /* accept actual antenna position unconditionally */
        g_az_seeded   = true;
        return v;
    }

    float delta = v - g_az_filtered;
    if (delta < 0.0f) { delta = -delta; }
    if (delta > ADC_MAX_DELTA) {
        if (++g_az_rejects >= ADC_NOISE_THRESHOLD) { g_fault = true; }
        return g_az_filtered;   /* reject; return last good value */
    }

    g_az_filtered = v;
    return v;
}

float adc_get_el(void)
{
    if (!g_primed) { return g_el_filtered; }

    uint32_t head = g_ring_head;
    float v = median_normalized(g_el_ring, head);

    if (!g_el_seeded) {
        g_el_filtered = v;
        g_el_seeded   = true;
        return v;
    }

    float delta = v - g_el_filtered;
    if (delta < 0.0f) { delta = -delta; }
    if (delta > ADC_MAX_DELTA) {
        if (++g_el_rejects >= ADC_NOISE_THRESHOLD) { g_fault = true; }
        return g_el_filtered;
    }

    g_el_filtered = v;
    return v;
}

bool adc_is_valid(void) { return !g_fault; }

void adc_clear_fault(void)
{
    g_fault       = false;
    g_az_rejects  = 0;
    g_el_rejects  = 0;
    g_window_tick = 0;
    /* Re-seed on next read so the filter accepts whatever position the
       antenna is at now rather than jumping straight back into a reject loop. */
    g_az_seeded   = false;
    g_el_seeded   = false;
}
