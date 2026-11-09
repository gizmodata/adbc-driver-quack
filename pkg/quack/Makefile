# Build helper for the cgo c-shared wrapper. The Python wheel build
# expects libadbc_driver_quack.{so,dylib,dll} produced by this Makefile.
#
# Targets:
#   make            — build the shared library for the current platform
#   make clean      — remove the shared library

ifeq ($(shell go env GOOS),linux)
	SUFFIX := so
else ifeq ($(shell go env GOOS),windows)
	SUFFIX := dll
else
	SUFFIX := dylib
endif

LIB := libadbc_driver_quack.$(SUFFIX)

.PHONY: all clean
all: $(LIB)

$(LIB): driver.go init.go utils.c utils.h adbc.h exports_linux.map exports_darwin.txt exports_windows.def ../../go.mod ../../go.sum ../../scripts/build_lib.sh
	../../scripts/build_lib.sh pkg/quack/$(LIB)

clean:
	rm -f $(LIB) libadbc_driver_quack.h
