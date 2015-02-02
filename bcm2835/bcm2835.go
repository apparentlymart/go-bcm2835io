// +build linux

// Package bcm2835 provides access to some of the external I/O interfaces on
// the Broadcom 2835 system-on-chip, which is most commonly known for its use
// as the brains behind the Raspberry Pi series of single-board computers.
//
// At present this driver supports only Linux targets, since it assumes the
// /dev/mem interface and the memory layout provided in the BCM2835 stock
// Linux kernel. (If you're running stock Raspian on a Raspberry Pi then this
// should work just fine for you.)
//
// On most systems, programs using this module will need to be run as root,
// since normal users do not generally have direct access to memory via
// /dev/mem.
package bcm2835

import (
	"errors"
	"github.com/apparentlymart/go-gpio/gpio"
	"github.com/apparentlymart/go-linuxgpio/linuxgpio"
	"os"
	"reflect"
	"syscall"
	"time"
	"unsafe"
)

const (
	peripheralBase = 0x20000000
	gpioBase       = peripheralBase + 0x200000
	gpioLength     = 4096
)

var (
	mem32 []uint32
	mem8  []uint8
)

// GpioPin is an extension of gpio.Pin and gpio.Puller that adds some
// additional methods that are unique to BCM2835 GPIO pins.
type GpioPin interface {
	gpio.Pin
	gpio.Puller

	// Number returns the GPIO number that this instance controls.
	Number() int

	// MakeLinuxGpioNode returns a linuxgpio.Node representing the Linux
	// sysfs endpoint corresponding to this GPIO. This allows access to
	// capabilities that are exposed via sysfs and that are not yet supported
	// by this library, such as edge waiting.
	MakeLinuxGpioNode() (node linuxgpio.Node)
}

// Manager is the main access point for BCM2835 I/O peripherals. Obtain a
// manager using the Open function.
type Manager interface {
	// GpioPin returns the GpioPin for the GPIO with the given number.
	GpioPin(number int) (pin GpioPin)

	// Close frees the resources allocated when opening this manager.
	// Once this is called, this manager and any child objects created from it
	// may no longer be used and references to them should be discarded.
	Close() error
}

type manager struct{}

type gpioPin int

// Open prepares to access the I/O peripherals and returns a Manager.
// Only one Manager can be open at once, so it should be opened early
// in the execution of a program and used to gain access to specific
// peripheral drivers.
func Open() (Manager, error) {
	if mem8 != nil {
		return nil, errors.New("BCM2835 already open")
	}
	mgr := manager{}

	file, err := os.OpenFile("/dev/mem", os.O_RDWR|os.O_SYNC, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	mem8, err = syscall.Mmap(
		int(file.Fd()),
		gpioBase,
		gpioLength,
		syscall.PROT_READ|syscall.PROT_WRITE,
		syscall.MAP_SHARED,
	)
	if err != nil {
		return nil, err
	}

	// Convert mem8 into a uint32 slice by tinkering with its guts.
	rawMem32 := *(*reflect.SliceHeader)(unsafe.Pointer(&mem8))
	rawMem32.Len /= 4 // four bytes per uint32
	rawMem32.Cap /= 4

	mem32 = *(*[]uint32)(unsafe.Pointer(&rawMem32))

	return &mgr, nil
}

// Close frees global resources associated with this package, invalidating
// the Manager. Callers must call Open to obtain a new Manager if devices need
// to be accessed again.
func (mgr *manager) Close() error {
	err := syscall.Munmap(mem8)
	if err != nil {
		return err
	}

	mem8 = nil
	mem32 = nil
	return nil
}

// GpioPin returns the GPIO pin with the given number.
func (mgr *manager) GpioPin(number int) GpioPin {
	return gpioPin(number)
}

// MakeLinuxGpioNode constructs a linuxgpio.Node object referring to the
// same GPIO.
func (pin gpioPin) MakeLinuxGpioNode() linuxgpio.Node {
	return linuxgpio.MakeNode(int(pin))
}

func (pin gpioPin) Number() int {
	return int(pin)
}

func (pin gpioPin) setPull(value uint32) {
	number := uint32(pin)

	// Pull clock signals are one bit per GPIO starting at offset 38.
	clockOffset := 38 + (number / 32)
	clockBit := number % 32

	// Pull value is in the register at offset 37
	const valueOffset = 37

	mem32[valueOffset] = (mem32[valueOffset] & ^uint32(3)) | value

	// Hardware docs call for us to pause briefly here.
	shortWait()

	mem32[clockOffset] = 1 << clockBit

	// Hardware docs call for us to pause briefly here.
	shortWait()

	mem32[valueOffset] = mem32[valueOffset] & ^uint32(3)
	mem32[clockOffset] = 0
}

func (pin gpioPin) PullDown() error {
	pin.setPull(1)
	return nil
}

func (pin gpioPin) PullUp() error {
	pin.setPull(2)
	return nil
}

func (pin gpioPin) StopPulling() error {
	pin.setPull(0)
	return nil
}

func (pin gpioPin) SetDirection(direction gpio.Direction) error {
	number := uint32(pin)

	// Modes are encoded as an array of three-bit values.
	selOffset := number / 10
	selBit := (number % 10) * 3

	var value uint32
	if direction == gpio.In {
		value = 0
	} else {
		value = 1 << selBit
	}

	mask := ^uint32(7 << selBit) // (three bits starting at our selection bit)

	mem32[selOffset] = (mem32[selOffset] & mask) | value

	return nil
}

func (pin gpioPin) SetValue(value gpio.Value) error {
	number := uint32(pin)

	offset := number / 32
	bit := number % 32
	if value == gpio.Low {
		offset += 10 // clear registers start at 10
	} else {
		offset += 7 // set registers start at 7
	}

	mem32[offset] = 1 << bit
	return nil
}

func (pin gpioPin) Value() (gpio.Value, error) {
	number := uint32(pin)

	// value registers start at 13, with one bit per GPIO
	offset := (number / 32) + 13
	bit := number % 32

	if (mem32[offset] & (1 << bit)) != 0 {
		return gpio.High, nil
	} else {
		return gpio.Low, nil
	}
}

func shortWait() {
	time.Sleep(time.Microsecond)
}
