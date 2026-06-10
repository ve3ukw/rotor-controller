# Rotor Controller

IP-connected antenna rotator controller for the **Yaesu G-5500DC** (and similar
az/el rotators).  Three self-contained tiers — a firmware field unit, a Go brain
daemon, and a set of command-line tools — with a rotctld-compatible shim for
satellite-tracking software such as SkyRoof, gpredict, and WSJT-X.

```
SkyRoof / gpredict / WSJT-X
        │ rotctld :4533
  rotor-hamlib
        │ REST / WebSocket
  rotor-brain  ←──── rotor (CLI)
        │ TCP :7700
  field unit (TM4C123 + W5500)
        │ contact closures / ADC
  G-5500DC rotator controller
```

---

## Hardware

| Component | Part |
|-----------|------|
| MCU board | Texas Instruments EK-TM4C123GXL LaunchPad |
| Ethernet | WIZnet W5500 module via BOOSTXL-IOBKOUT BoosterPack |
| Rotator | Yaesu G-5500DC (or G-5500) |

**GPIO assignments (abbreviated):**

| Signal | MCU pin | BoosterPack |
|--------|---------|-------------|
| AZ pot wiper | PD3 (AIN4) | AZ |
| EL pot wiper | PE1 (AIN2) | AY |
| AZ CW relay | PE3 | A3 |
| AZ CCW relay | PE4 | A4 |
| EL UP relay | PF4 | A1 |
| EL DOWN relay | PE0 | A2 |
| Hardware ESTOP | PB1 | A9 (active LOW, latching) |
| Park trigger | PB0 | A8 (falling edge) |

See `hardware/gpio-mapping.txt` for the complete pin table.

---

## Network defaults

| Setting | Value |
|---------|-------|
| Field unit IP | `192.168.1.5` |
| TCP port (commands) | `7700` |
| Brain HTTP / WebSocket | `:8090` |
| rotctld shim | `:4533` |

All of these are configurable at runtime — see [Network configuration](#network-configuration).

---

## Quick start

### 1. Flash the field unit

```bash
cd controller
cmake -B build -DCMAKE_TOOLCHAIN_FILE=cmake/arm-none-eabi.cmake
cmake --build build --target flash
```

Requires `arm-none-eabi-gcc` and OpenOCD.

### 2. Start the brain

**Linux / WSL2:**
```bash
./bin/rotor-brain
```

**Windows** (build natively to avoid SmartScreen):
```powershell
cd \\wsl.localhost\Ubuntu\home\marcel\src\rotor-controller\middleware
go build -o bin\rotor-brain.exe .\cmd\brain
.\bin\rotor-brain.exe
```

The brain connects to `192.168.1.5:7700` and logs:
```
12:34:56.123 rotor-brain dev starting
12:34:56.124 config: field unit 192.168.1.5:7700  http :8090
12:34:56.130 fieldunit: connected to 192.168.1.5:7700
12:34:56.131 fieldunit: link UP
```

### 3. Check status

```bash
./bin/rotor status
```
```
Field unit : LINKED
Age        : 12ms
State      : IDLE
AZ         : 180.0°  (0.4000)
EL         :  45.0°  (0.2500)
Motion     : az=stop  el=stop
RF switches: VHF=off  UHF=off  LNA=off  RXTX=off
Duty cycle : AZ  0%  EL  0%
```

---

## Brain configuration

The brain reads config from (highest priority wins):

1. Environment variable `BRAIN_FIELD_UNIT_HOST`, `BRAIN_HTTP_ADDR`, etc.
2. `rotor-brain.json` next to the executable  ← **simplest for Windows**
3. `%APPDATA%\rotor\brain.json` (Windows)
4. `~/.rotor-brain.json` (Linux / macOS)

**Minimal config file** (`rotor-brain.json`):
```json
{
  "field_unit_host": "192.168.1.5"
}
```

`rotor-brain --help` lists all options and shows the resolved config file path.

---

## CLI reference (`rotor`)

```
rotor [--brain URL] [-v] <command> [args]
```

`--brain` overrides the brain URL for one command.  
`-v` / `--verbose` prints HTTP requests and responses to stderr.

### Motion

```bash
rotor move cw stop        # AZ clockwise, EL stop
rotor move ccw down       # AZ counter-clockwise, EL down
rotor move stop stop      # stop all motion
rotor park                # drive to park position (AZ 180° / EL 45°)
rotor estop               # emergency stop (software)
rotor fault               # clear fault / acknowledge hardware ESTOP
```

### Status and monitoring

```bash
rotor status              # snapshot: position, state, RF switches, duty
rotor monitor             # live stream at 1 Hz (averaged)
rotor monitor -rate 5     # 5 Hz
rotor monitor -rate 20    # raw 20 Hz frames
rotor monitor -rate 0.2   # one line every 5 seconds
```

### RF switches

```bash
rotor pol vhf uhf         # enable VHF + UHF polarization switches
rotor pol lna             # enable LNA only
rotor pol off             # all switches off
```

### Travel limits

Limits are normalised 0–1 (not degrees).  Default: full travel.

```bash
rotor limits --az-min 0.05 --az-max 0.95 --el-min 0.0 --el-max 1.0
```

---

## Obstacle avoidance (`rotor block`)

The AZ range is divided into **90 sectors of 5°** each.  Each sector can have a
minimum elevation floor — the antenna will not be driven below that floor while
in that azimuth sector.  Useful for buildings, trees, and grumpy neighbours.

### Setting a floor

```bash
# Require ≥ 20° elevation when AZ is in the 45°–50° sector
rotor block set --az 45 --el 20
```

### Training mode

Point the antenna at the edge of the obstacle, then:

```bash
rotor block train              # records current AZ sector → current EL floor
rotor block train --margin 5   # adds 5° safety margin above recorded EL
```

Repeat for each sector along the obstacle boundary.

### Reviewing blocks

```bash
rotor block show               # list non-zero sectors

rotor block show --map         # ASCII horizon map
```

Example map output:
```
EL
90°│·············································
81°│·············································
72°│·············································
63°│·············································
54°│·············································
45°│·············████████·····················
36°│·········████████████████···················
27°│·····████████████████████████·············
18°│···█████████████████████████████···········
 9°│·█████████████████████████████████·········
   └─────────────────────────────────────────────
   0   45  90  135 180 225 270 315 360 405 450° AZ
```

### Removing blocks

```bash
rotor block clear --az 45      # clear one sector
rotor block clear-all          # remove all floors
```

### How enforcement works

When the antenna is commanded EL DOWN and the current AZ sector has a floor,
elevation motion stops at the floor.  Azimuth continues moving.  No fault is
raised — this is normal operating behaviour at an obstacle boundary.

---

## Hardware ESTOP (A9 / PB1)

A9 is a **latching** emergency stop:

| Event | Effect |
|-------|--------|
| A9 pulled LOW | All motion stops immediately. Display shows `[E-STOP!]`. |
| A9 released (HIGH) | Motion remains stopped. `[E-STOP!]` stays on display. |
| `rotor fault` | Clears the latch — motion commands accepted again. |
| Hold A8 (park) 3 s | Hardware-only latch clear when brain is not connected. |

The latch cannot be cleared while A9 is still LOW — the physical signal must be
released before any software acknowledge is accepted.

---

## Network configuration

Change the field unit's IP without reflashing:

```bash
rotor netconfig --ip 192.168.1.20 --subnet 255.255.255.0 --gateway 192.168.1.1
```

This:
1. Programs the new IP into the field unit's EEPROM (survives power cycles)
2. Updates the brain config file so `rotor-brain` connects to the new IP on restart

Restart the brain after changing the IP:
```bash
./bin/rotor-brain   # reads updated config file automatically
```

**Revert to factory defaults** (config.h values, next reboot):
```bash
rotor netconfig reset
```

---

## rotctld shim (`rotor-hamlib`)

Provides a **rotctld-compatible TCP server** on port 4533 for satellite-tracking
software (SkyRoof, gpredict, WSJT-X, fldigi, …).

```bash
./bin/rotor-hamlib              # connect to brain at localhost:8090
./bin/rotor-hamlib --help       # show all options
```

**Environment variables:**

| Variable | Default | Description |
|----------|---------|-------------|
| `HAMLIB_LISTEN` | `:4533` | TCP listen address |
| `HAMLIB_BRAIN_URL` | `http://localhost:8090` | Brain API |
| `HAMLIB_AZ_RANGE` | `450` | Full AZ travel (°) |
| `HAMLIB_EL_RANGE` | `180` | Full EL travel (°) |
| `HAMLIB_TOLERANCE` | `2.0` | Stop tolerance (°) |

**SkyRoof setup:** point the rotator connection to `localhost:4533`.

**Running on Linux / WSL2** (recommended over Windows .exe):
```bash
./bin/rotor-brain &
./bin/rotor-hamlib &
# SkyRoof on Windows connects to localhost:4533 via WSL2 mirrored networking
```

---

## Running on a Raspberry Pi

Cross-compile from the development machine:

```bash
make -C middleware build-pi    # produces bin/rotor-brain-pi, bin/rotor-pi, bin/rotor-hamlib-pi
```

Copy to the Pi and install as services:

```bash
scp middleware/bin/*-pi pi@raspberrypi.local:~/rotor/

# On the Pi — create /etc/systemd/system/rotor-brain.service
[Unit]
Description=Rotor Brain
After=network.target

[Service]
ExecStart=/home/pi/rotor/rotor-brain-pi
Restart=on-failure
Environment=BRAIN_FIELD_UNIT_HOST=192.168.1.5

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now rotor-brain
sudo systemctl enable --now rotor-hamlib   # similar service file
```

---

## Troubleshooting

### Field unit not reachable

```bash
ping 192.168.1.5                # is it on the network?
nc -zv 192.168.1.5 7700         # is TCP port open?
```

If ping works but port 7700 refuses: the field unit is running but not yet in
LISTEN state.  Wait ~3 seconds after boot or press the reset button (SW2).

### Brain connects but no telemetry

Run `rotor status` — if **Telemetry: (none)** check that the brain log shows
`blocks: pushed 90 sectors` (indicates the TCP session completed cleanly).

### Display shows garbled characters

The LCD initialises to 8-bit mode on power-on; it must receive the 4-bit init
sequence at its actual I2C address.  If garbled after a firmware change, check
that `display_init()` is called **after** `net_init()` in `main.c`.

### ESTOP won't clear

1. Verify A9 (PB1) is HIGH — check with a multimeter.
2. With brain connected: `rotor fault`
3. Without brain: hold A8 (park button) for 3 seconds while A9 is HIGH.
4. If still stuck: check that no G-5500 wiring is accidentally pulling PB1 to GND.

### Windows Defender blocks the .exe

Build natively on Windows — locally compiled binaries have no Zone.Identifier:
```powershell
go build -o rotor-hamlib.exe .\cmd\hamlib
```

Or run the Linux binaries inside WSL2 and connect from Windows via `localhost`.

---

## Building

### Firmware

```bash
# Prerequisites: arm-none-eabi-gcc, cmake, openocd
cd controller
cmake -B build -DCMAKE_TOOLCHAIN_FILE=cmake/arm-none-eabi.cmake
cmake --build build                    # compile
cmake --build build --target flash     # flash via OpenOCD
```

Debug logging (UART0, 115200 baud):
```bash
cmake -B build -DCMAKE_TOOLCHAIN_FILE=... -DENABLE_DEBUG_LOG=ON
```

### Brain, CLI, hamlib shim

```bash
cd middleware
make build           # Linux x86-64
make build-windows   # Windows x86-64 (cross-compile)
make build-pi        # Raspberry Pi arm64
make all             # all three platforms
```

Requires Go ≥ 1.22.  Set `GOTOOLCHAIN=local` to suppress automatic toolchain downloads.

---

*VE3UKW — built with ❤️ and considerably more patience than Windows deserves.*
