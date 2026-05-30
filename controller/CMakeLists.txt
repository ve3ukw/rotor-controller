cmake_minimum_required(VERSION 3.20)

# Toolchain must be resolved before project() — allow CLI override
if(NOT DEFINED CMAKE_TOOLCHAIN_FILE)
    set(CMAKE_TOOLCHAIN_FILE "${CMAKE_CURRENT_SOURCE_DIR}/cmake/arm-none-eabi.cmake"
        CACHE FILEPATH "Toolchain file")
endif()

project(rotor-controller C ASM)

set(CMAKE_C_STANDARD 11)
set(CMAKE_C_STANDARD_REQUIRED ON)

# ── TivaWare paths ────────────────────────────────────────────────────────────
set(TIVAWARE_DIR ${CMAKE_CURRENT_SOURCE_DIR}/lib/tivaware)

# ── WIZnet ioLibrary (W5500 only) ────────────────────────────────────────
set(W5500_DIR ${CMAKE_CURRENT_SOURCE_DIR}/lib/w5500)

add_library(wiznet STATIC
    ${W5500_DIR}/Ethernet/wizchip_conf.c
    ${W5500_DIR}/Ethernet/W5500/w5500.c
    ${W5500_DIR}/Ethernet/socket.c
)
target_include_directories(wiznet PUBLIC
    ${W5500_DIR}/Ethernet
    ${W5500_DIR}/Ethernet/W5500
)
target_compile_definitions(wiznet PUBLIC _WIZCHIP_=W5500)
# Vendored — suppress warnings we can't fix
target_compile_options(wiznet PRIVATE -Os -ffunction-sections -fdata-sections -w)

# ── driverlib static library ──────────────────────────────────────────────────
# Only the peripherals this project uses; add here as new steps introduce them.
add_library(driverlib STATIC
    ${TIVAWARE_DIR}/driverlib/adc.c
    ${TIVAWARE_DIR}/driverlib/cpu.c
    ${TIVAWARE_DIR}/driverlib/gpio.c
    ${TIVAWARE_DIR}/driverlib/interrupt.c
    ${TIVAWARE_DIR}/driverlib/ssi.c
    ${TIVAWARE_DIR}/driverlib/sysctl.c
    ${TIVAWARE_DIR}/driverlib/systick.c
    ${TIVAWARE_DIR}/driverlib/timer.c
    ${TIVAWARE_DIR}/driverlib/uart.c
    ${TIVAWARE_DIR}/driverlib/udma.c
    ${TIVAWARE_DIR}/driverlib/watchdog.c
)

target_include_directories(driverlib PUBLIC ${TIVAWARE_DIR})
# "gcc" selects the GCC branch in TivaWare toolchain guards (e.g. SysCtlDelay)
target_compile_definitions(driverlib PUBLIC PART_TM4C123GH6PM gcc)

# Vendored code — suppress pedantic warnings
target_compile_options(driverlib PRIVATE -Os -ffunction-sections -fdata-sections)

# ── firmware executable ───────────────────────────────────────────────────────
add_executable(rotor-controller.elf
    src/startup_gcc.c
    src/main.c
    src/tick.c
    src/adc.c
    src/gpio_outputs.c
    src/state_machine.c
    src/net.c
    src/protocol.c
)

# ── optional debug UART (UART0 / ICDI virtual COM, 115200) ────────────────
option(ENABLE_DEBUG_LOG "Enable UARTprintf debug logging over UART0" OFF)
if(ENABLE_DEBUG_LOG)
    target_compile_definitions(rotor-controller.elf PRIVATE DEBUG_LOG=1)
    target_sources(rotor-controller.elf PRIVATE
        ${TIVAWARE_DIR}/utils/uartstdio.c
    )
    target_include_directories(rotor-controller.elf PRIVATE
        ${TIVAWARE_DIR}/utils
    )
    message(STATUS "Debug logging enabled (UART0 115200)")
endif()

target_include_directories(rotor-controller.elf PRIVATE
    ${TIVAWARE_DIR}
    ${W5500_DIR}/Ethernet
    ${W5500_DIR}/Ethernet/W5500
    src
)

target_compile_definitions(rotor-controller.elf PRIVATE
    PART_TM4C123GH6PM
    gcc
)

target_compile_options(rotor-controller.elf PRIVATE
    -Wall
    -Wextra
    -Os
    -ffunction-sections
    -fdata-sections
    -fno-common
)

target_link_options(rotor-controller.elf PRIVATE
    -T${CMAKE_CURRENT_SOURCE_DIR}/src/tm4c123.ld
    -Wl,--gc-sections
    -Wl,-Map=${CMAKE_CURRENT_BINARY_DIR}/rotor-controller.map
    --specs=nosys.specs
    --specs=nano.specs
)

target_link_libraries(rotor-controller.elf PRIVATE driverlib wiznet m)

# ── post-build: .bin image + size report ─────────────────────────────────────
add_custom_command(TARGET rotor-controller.elf POST_BUILD
    COMMAND ${CMAKE_OBJCOPY} -O binary
        $<TARGET_FILE:rotor-controller.elf>
        ${CMAKE_CURRENT_BINARY_DIR}/rotor-controller.bin
    COMMENT "Generating rotor-controller.bin"
)

add_custom_command(TARGET rotor-controller.elf POST_BUILD
    COMMAND ${CMAKE_SIZE} $<TARGET_FILE:rotor-controller.elf>
)

# ── flash target: cmake --build build --target flash ─────────────────────────
add_custom_target(flash
    COMMAND openocd
        -f ${CMAKE_CURRENT_SOURCE_DIR}/openocd/tm4c123.cfg
        -c "program $<TARGET_FILE:rotor-controller.elf> verify reset exit"
    DEPENDS rotor-controller.elf
    COMMENT "Flashing via OpenOCD"
)
