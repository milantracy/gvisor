// Copyright 2018 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package syserr contains sandbox-internal errors. These errors are distinct
// from both the errors returned by host system calls and the errors returned
// to sandboxed applications.
package syserr

import (
	"fmt"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/abi/linux/errno"
	"gvisor.dev/gvisor/pkg/errors"
	"gvisor.dev/gvisor/pkg/errors/linuxerr"
	"gvisor.dev/gvisor/pkg/safecopy"
)

// Error represents an internal error.
type Error struct {
	// message is the human readable form of this Error.
	message string

	// noTranslation indicates that this Error cannot be translated to a
	// errno.Errno.
	noTranslation bool

	// errno is the errno.Errno this Error should be translated to.
	errno errno.Errno
}

// New creates a new Error and adds a translation for it.
//
// New must only be called at init.
func New(message string, linuxTranslation errno.Errno) *Error {
	err := &Error{message: message, errno: linuxTranslation}

	// TODO(b/34162363): Remove this.
	if int(err.errno) >= len(linuxBackwardsTranslations) {
		panic(fmt.Sprint("invalid errno: ", err.errno))
	}

	e := backwardsError(err.errno, message)
	// linuxerr.ErrWouldBlock gets translated to linuxerr.EWOULDBLOCK and
	// enables proper blocking semantics. This should temporary address the
	// class of blocking bugs that keep popping up with the current state of
	// the error space.
	if err.errno == linuxerr.EWOULDBLOCK.Errno() {
		e = linuxerr.ErrWouldBlock
	}
	linuxBackwardsTranslations[err.errno] = linuxBackwardsTranslation{err: e, ok: true}

	return err
}

// NewDynamic creates a new error with a dynamic error message and an errno
// translation.
//
// NewDynamic should only be used sparingly and not be used for static error
// messages. Errors with static error messages should be declared with New as
// global variables.
func NewDynamic(message string, linuxTranslation errno.Errno) *Error {
	return &Error{message: message, errno: linuxTranslation}
}

func newWithHost(message string, linuxTranslation errno.Errno, hostErrno unix.Errno) *Error {
	e := New(message, linuxTranslation)
	addHostTranslation(hostErrno, e)
	addLinuxTranslation(linuxTranslation, e)
	return e
}

// newLinuxOnly is like newWithHost, for an errno that only Linux has.
func newLinuxOnly(message string, linuxTranslation errno.Errno) *Error {
	e := New(message, linuxTranslation)
	addLinuxOnlyHostTranslation(linuxTranslation, e)
	addLinuxTranslation(linuxTranslation, e)
	return e
}

// maxLinuxErrno is one more than the largest Linux errno.
const maxLinuxErrno = errno.EHWPOISON + 1

// linuxTranslations maps a Linux errno to its Error.
var linuxTranslations [maxLinuxErrno]*Error

func addLinuxTranslation(linuxErrno errno.Errno, trans *Error) {
	if linuxTranslations[linuxErrno] != nil {
		panic(fmt.Sprintf("duplicate translation for Linux errno %d", linuxErrno))
	}
	linuxTranslations[linuxErrno] = trans
}

// fromLinux translates a Linux errno to its Error.
func fromLinux(linuxErrno errno.Errno) *Error {
	if linuxErrno >= maxLinuxErrno || linuxTranslations[linuxErrno] == nil {
		panic(fmt.Sprintf("unknown Linux errno %d", linuxErrno))
	}
	return linuxTranslations[linuxErrno]
}

// String implements fmt.Stringer.String.
func (e *Error) String() string {
	if e == nil {
		return "<nil>"
	}
	return e.message
}

type linuxBackwardsTranslation struct {
	err error
	ok  bool
}

// TODO(b/34162363): Remove this.
var linuxBackwardsTranslations [maxLinuxErrno]linuxBackwardsTranslation

// ToError translates an Error to a corresponding error value.
//
// TODO(b/34162363): Remove this.
func (e *Error) ToError() error {
	if e == nil {
		return nil
	}
	if e.noTranslation {
		panic(fmt.Sprintf("error %q does not support translation", e.message))
	}
	err := int(e.errno)
	if err == errno.NOERRNO {
		return nil
	}
	if err >= len(linuxBackwardsTranslations) || !linuxBackwardsTranslations[err].ok {
		panic(fmt.Sprintf("unknown error %q (%d)", e.message, err))
	}
	return linuxBackwardsTranslations[err].err
}

// ToLinux converts the Error to a Linux ABI error that can be returned to the
// application.
func (e *Error) ToLinux() errno.Errno {
	if e.noTranslation {
		panic(fmt.Sprintf("No Linux ABI translation available for %q", e.message))
	}
	return e.errno
}

// AnnotatedError represents an error with an additional message.
type AnnotatedError struct {
	error   *Error
	message string
}

// Error implements Error() for the error interface
func (e *AnnotatedError) Error() string {
	return fmt.Sprintf("%s: %s", e.error.String(), e.message)
}

// NewAnnotatedError creates a new AnnotatedError with the given error and message.
func NewAnnotatedError(error *Error, message string) *AnnotatedError {
	return &AnnotatedError{error: error, message: message}
}

// GetError returns the underlying error.
func (e *AnnotatedError) GetError() *Error {
	return e.error
}

// TODO(b/34162363): Remove or replace most of these errors.
//
// Some of the errors should be replaced with package specific errors and
// others should be removed entirely.
//
// Note that some errors are declared in platform-specific files.
var (
	ErrNotPermitted               = newWithHost("operation not permitted", errno.EPERM, unix.EPERM)
	ErrNoFileOrDir                = newWithHost("no such file or directory", errno.ENOENT, unix.ENOENT)
	ErrNoProcess                  = newWithHost("no such process", errno.ESRCH, unix.ESRCH)
	ErrInterrupted                = newWithHost("interrupted system call", errno.EINTR, unix.EINTR)
	ErrIO                         = newWithHost("I/O error", errno.EIO, unix.EIO)
	ErrDeviceOrAddress            = newWithHost("no such device or address", errno.ENXIO, unix.ENXIO)
	ErrTooManyArgs                = newWithHost("argument list too long", errno.E2BIG, unix.E2BIG)
	ErrEcec                       = newWithHost("exec format error", errno.ENOEXEC, unix.ENOEXEC)
	ErrBadFD                      = newWithHost("bad file number", errno.EBADF, unix.EBADF)
	ErrNoChild                    = newWithHost("no child processes", errno.ECHILD, unix.ECHILD)
	ErrTryAgain                   = newWithHost("try again", errno.EAGAIN, unix.EAGAIN)
	ErrNoMemory                   = newWithHost("out of memory", errno.ENOMEM, unix.ENOMEM)
	ErrPermissionDenied           = newWithHost("permission denied", errno.EACCES, unix.EACCES)
	ErrBadAddress                 = newWithHost("bad address", errno.EFAULT, unix.EFAULT)
	ErrNotBlockDevice             = newWithHost("block device required", errno.ENOTBLK, unix.ENOTBLK)
	ErrBusy                       = newWithHost("device or resource busy", errno.EBUSY, unix.EBUSY)
	ErrExists                     = newWithHost("file exists", errno.EEXIST, unix.EEXIST)
	ErrCrossDeviceLink            = newWithHost("cross-device link", errno.EXDEV, unix.EXDEV)
	ErrNoDevice                   = newWithHost("no such device", errno.ENODEV, unix.ENODEV)
	ErrNotDir                     = newWithHost("not a directory", errno.ENOTDIR, unix.ENOTDIR)
	ErrIsDir                      = newWithHost("is a directory", errno.EISDIR, unix.EISDIR)
	ErrInvalidArgument            = newWithHost("invalid argument", errno.EINVAL, unix.EINVAL)
	ErrFileTableOverflow          = newWithHost("file table overflow", errno.ENFILE, unix.ENFILE)
	ErrTooManyOpenFiles           = newWithHost("too many open files", errno.EMFILE, unix.EMFILE)
	ErrNotTTY                     = newWithHost("not a typewriter", errno.ENOTTY, unix.ENOTTY)
	ErrTestFileBusy               = newWithHost("text file busy", errno.ETXTBSY, unix.ETXTBSY)
	ErrFileTooBig                 = newWithHost("file too large", errno.EFBIG, unix.EFBIG)
	ErrNoSpace                    = newWithHost("no space left on device", errno.ENOSPC, unix.ENOSPC)
	ErrIllegalSeek                = newWithHost("illegal seek", errno.ESPIPE, unix.ESPIPE)
	ErrReadOnlyFS                 = newWithHost("read-only file system", errno.EROFS, unix.EROFS)
	ErrTooManyLinks               = newWithHost("too many links", errno.EMLINK, unix.EMLINK)
	ErrBrokenPipe                 = newWithHost("broken pipe", errno.EPIPE, unix.EPIPE)
	ErrDomain                     = newWithHost("math argument out of domain of func", errno.EDOM, unix.EDOM)
	ErrRange                      = newWithHost("math result not representable", errno.ERANGE, unix.ERANGE)
	ErrNameTooLong                = newWithHost("file name too long", errno.ENAMETOOLONG, unix.ENAMETOOLONG)
	ErrNoLocksAvailable           = newWithHost("no record locks available", errno.ENOLCK, unix.ENOLCK)
	ErrInvalidSyscall             = newWithHost("invalid system call number", errno.ENOSYS, unix.ENOSYS)
	ErrDirNotEmpty                = newWithHost("directory not empty", errno.ENOTEMPTY, unix.ENOTEMPTY)
	ErrLinkLoop                   = newWithHost("too many symbolic links encountered", errno.ELOOP, unix.ELOOP)
	ErrNoMessage                  = newWithHost("no message of desired type", errno.ENOMSG, unix.ENOMSG)
	ErrIdentifierRemoved          = newWithHost("identifier removed", errno.EIDRM, unix.EIDRM)
	ErrNotStream                  = newWithHost("device not a stream", errno.ENOSTR, unix.ENOSTR)
	ErrNoDataAvailable            = newWithHost("no data available", errno.ENODATA, unix.ENODATA)
	ErrTimerExpired               = newWithHost("timer expired", errno.ETIME, unix.ETIME)
	ErrStreamsResourceDepleted    = newWithHost("out of streams resources", errno.ENOSR, unix.ENOSR)
	ErrIsRemote                   = newWithHost("object is remote", errno.EREMOTE, unix.EREMOTE)
	ErrNoLink                     = newWithHost("link has been severed", errno.ENOLINK, unix.ENOLINK)
	ErrProtocol                   = newWithHost("protocol error", errno.EPROTO, unix.EPROTO)
	ErrMultihopAttempted          = newWithHost("multihop attempted", errno.EMULTIHOP, unix.EMULTIHOP)
	ErrInvalidDataMessage         = newWithHost("not a data message", errno.EBADMSG, unix.EBADMSG)
	ErrOverflow                   = newWithHost("value too large for defined data type", errno.EOVERFLOW, unix.EOVERFLOW)
	ErrIllegalByteSequence        = newWithHost("illegal byte sequence", errno.EILSEQ, unix.EILSEQ)
	ErrTooManyUsers               = newWithHost("too many users", errno.EUSERS, unix.EUSERS)
	ErrNotASocket                 = newWithHost("socket operation on non-socket", errno.ENOTSOCK, unix.ENOTSOCK)
	ErrDestinationAddressRequired = newWithHost("destination address required", errno.EDESTADDRREQ, unix.EDESTADDRREQ)
	ErrMessageTooLong             = newWithHost("message too long", errno.EMSGSIZE, unix.EMSGSIZE)
	ErrWrongProtocolForSocket     = newWithHost("protocol wrong type for socket", errno.EPROTOTYPE, unix.EPROTOTYPE)
	ErrProtocolNotAvailable       = newWithHost("protocol not available", errno.ENOPROTOOPT, unix.ENOPROTOOPT)
	ErrProtocolNotSupported       = newWithHost("protocol not supported", errno.EPROTONOSUPPORT, unix.EPROTONOSUPPORT)
	ErrSocketNotSupported         = newWithHost("socket type not supported", errno.ESOCKTNOSUPPORT, unix.ESOCKTNOSUPPORT)
	ErrEndpointOperation          = newWithHost("operation not supported on transport endpoint", errno.EOPNOTSUPP, unix.EOPNOTSUPP)
	ErrProtocolFamilyNotSupported = newWithHost("protocol family not supported", errno.EPFNOSUPPORT, unix.EPFNOSUPPORT)
	ErrAddressFamilyNotSupported  = newWithHost("address family not supported by protocol", errno.EAFNOSUPPORT, unix.EAFNOSUPPORT)
	ErrAddressInUse               = newWithHost("address already in use", errno.EADDRINUSE, unix.EADDRINUSE)
	ErrAddressNotAvailable        = newWithHost("cannot assign requested address", errno.EADDRNOTAVAIL, unix.EADDRNOTAVAIL)
	ErrNetworkDown                = newWithHost("network is down", errno.ENETDOWN, unix.ENETDOWN)
	ErrNetworkUnreachable         = newWithHost("network is unreachable", errno.ENETUNREACH, unix.ENETUNREACH)
	ErrNetworkReset               = newWithHost("network dropped connection because of reset", errno.ENETRESET, unix.ENETRESET)
	ErrConnectionAborted          = newWithHost("software caused connection abort", errno.ECONNABORTED, unix.ECONNABORTED)
	ErrConnectionReset            = newWithHost("connection reset by peer", errno.ECONNRESET, unix.ECONNRESET)
	ErrNoBufferSpace              = newWithHost("no buffer space available", errno.ENOBUFS, unix.ENOBUFS)
	ErrAlreadyConnected           = newWithHost("transport endpoint is already connected", errno.EISCONN, unix.EISCONN)
	ErrNotConnected               = newWithHost("transport endpoint is not connected", errno.ENOTCONN, unix.ENOTCONN)
	ErrShutdown                   = newWithHost("cannot send after transport endpoint shutdown", errno.ESHUTDOWN, unix.ESHUTDOWN)
	ErrTooManyRefs                = newWithHost("too many references: cannot splice", errno.ETOOMANYREFS, unix.ETOOMANYREFS)
	ErrTimedOut                   = newWithHost("connection timed out", errno.ETIMEDOUT, unix.ETIMEDOUT)
	ErrConnectionRefused          = newWithHost("connection refused", errno.ECONNREFUSED, unix.ECONNREFUSED)
	ErrHostDown                   = newWithHost("host is down", errno.EHOSTDOWN, unix.EHOSTDOWN)
	ErrHostUnreachable            = newWithHost("no route to host", errno.EHOSTUNREACH, unix.EHOSTUNREACH)
	ErrAlreadyInProgress          = newWithHost("operation already in progress", errno.EALREADY, unix.EALREADY)
	ErrInProgress                 = newWithHost("operation now in progress", errno.EINPROGRESS, unix.EINPROGRESS)
	ErrStaleFileHandle            = newWithHost("stale file handle", errno.ESTALE, unix.ESTALE)
	ErrQuotaExceeded              = newWithHost("quota exceeded", errno.EDQUOT, unix.EDQUOT)
	ErrCanceled                   = newWithHost("operation canceled", errno.ECANCELED, unix.ECANCELED)
	ErrOwnerDied                  = newWithHost("owner died", errno.EOWNERDEAD, unix.EOWNERDEAD)
	ErrNotRecoverable             = newWithHost("state not recoverable", errno.ENOTRECOVERABLE, unix.ENOTRECOVERABLE)

	// ErrWouldBlock translates to EWOULDBLOCK which is the same as EAGAIN
	// on Linux.
	ErrWouldBlock = New("operation would block", errno.EWOULDBLOCK)
)

// FromHost translates a unix.Errno to a corresponding Error value.
func FromHost(err unix.Errno) *Error {
	got := getHostTranslation(err)
	if got == nil {
		panic(fmt.Sprintf("unknown host errno %q (%d)", err.Error(), err))
	}
	return got
}

// IsValid checks if the given errno is a valid errno which can be translated
// to an Error.
func IsValid(err unix.Errno) bool {
	return getHostTranslation(err) != nil
}

// FromError converts a generic error to an *Error.
//
// TODO(b/34162363): Remove this function.
func FromError(err error) *Error {
	if err == nil {
		return nil
	}

	switch e := err.(type) {
	case unix.Errno:
		return FromHost(e)
	case *errors.Error:
		if linuxerr.IsRestartError(e) {
			return ErrInterrupted
		}
		return fromLinux(e.Errno())
	case safecopy.SegvError, safecopy.BusError, safecopy.AlignmentError:
		return FromHost(unix.EFAULT)
	}

	msg := fmt.Sprintf("err: %s type: %T", err.Error(), err)
	panic(msg)
}

// Errors for errnos that only Linux has. On other hosts, there is no host
// errno for them.
//
// TODO(b/34162363): Remove or replace most of these errors.
//
// Some of the errors should be replaced with package specific errors and
// others should be removed entirely.
var (
	ErrDeadlock                  = newLinuxOnly("resource deadlock would occur", errno.EDEADLOCK)
	ErrChannelOutOfRange         = newLinuxOnly("channel number out of range", errno.ECHRNG)
	ErrLevelTwoNotSynced         = newLinuxOnly("level 2 not synchronized", errno.EL2NSYNC)
	ErrLevelThreeHalted          = newLinuxOnly("level 3 halted", errno.EL3HLT)
	ErrLevelThreeReset           = newLinuxOnly("level 3 reset", errno.EL3RST)
	ErrLinkNumberOutOfRange      = newLinuxOnly("link number out of range", errno.ELNRNG)
	ErrProtocolDriverNotAttached = newLinuxOnly("protocol driver not attached", errno.EUNATCH)
	ErrNoCSIAvailable            = newLinuxOnly("no CSI structure available", errno.ENOCSI)
	ErrLevelTwoHalted            = newLinuxOnly("level 2 halted", errno.EL2HLT)
	ErrInvalidExchange           = newLinuxOnly("invalid exchange", errno.EBADE)
	ErrInvalidRequestDescriptor  = newLinuxOnly("invalid request descriptor", errno.EBADR)
	ErrExchangeFull              = newLinuxOnly("exchange full", errno.EXFULL)
	ErrNoAnode                   = newLinuxOnly("no anode", errno.ENOANO)
	ErrInvalidRequestCode        = newLinuxOnly("invalid request code", errno.EBADRQC)
	ErrInvalidSlot               = newLinuxOnly("invalid slot", errno.EBADSLT)
	ErrBadFontFile               = newLinuxOnly("bad font file format", errno.EBFONT)
	ErrMachineNotOnNetwork       = newLinuxOnly("machine is not on the network", errno.ENONET)
	ErrPackageNotInstalled       = newLinuxOnly("package not installed", errno.ENOPKG)
	ErrAdvertise                 = newLinuxOnly("advertise error", errno.EADV)
	ErrSRMount                   = newLinuxOnly("srmount error", errno.ESRMNT)
	ErrSendCommunication         = newLinuxOnly("communication error on send", errno.ECOMM)
	ErrRFS                       = newLinuxOnly("RFS specific error", errno.EDOTDOT)
	ErrNetworkNameNotUnique      = newLinuxOnly("name not unique on network", errno.ENOTUNIQ)
	ErrFDInBadState              = newLinuxOnly("file descriptor in bad state", errno.EBADFD)
	ErrRemoteAddressChanged      = newLinuxOnly("remote address changed", errno.EREMCHG)
	ErrSharedLibraryInaccessible = newLinuxOnly("can not access a needed shared library", errno.ELIBACC)
	ErrCorruptedSharedLibrary    = newLinuxOnly("accessing a corrupted shared library", errno.ELIBBAD)
	ErrLibSectionCorrupted       = newLinuxOnly(".lib section in a.out corrupted", errno.ELIBSCN)
	ErrTooManySharedLibraries    = newLinuxOnly("attempting to link in too many shared libraries", errno.ELIBMAX)
	ErrSharedLibraryExeced       = newLinuxOnly("cannot exec a shared library directly", errno.ELIBEXEC)
	ErrShouldRestart             = newLinuxOnly("interrupted system call should be restarted", errno.ERESTART)
	ErrStreamPipe                = newLinuxOnly("streams pipe error", errno.ESTRPIPE)
	ErrStructureNeedsCleaning    = newLinuxOnly("structure needs cleaning", errno.EUCLEAN)
	ErrIsNotNamedFile            = newLinuxOnly("not a XENIX named type file", errno.ENOTNAM)
	ErrNotAvailable              = newLinuxOnly("no XENIX semaphores available", errno.ENAVAIL)
	ErrIsNamedFile               = newLinuxOnly("is a named type file", errno.EISNAM)
	ErrRemoteIO                  = newLinuxOnly("remote I/O error", errno.EREMOTEIO)
	ErrNoMedium                  = newLinuxOnly("no medium found", errno.ENOMEDIUM)
	ErrWrongMediumType           = newLinuxOnly("wrong medium type", errno.EMEDIUMTYPE)
	ErrNoKey                     = newLinuxOnly("required key not available", errno.ENOKEY)
	ErrKeyExpired                = newLinuxOnly("key has expired", errno.EKEYEXPIRED)
	ErrKeyRevoked                = newLinuxOnly("key has been revoked", errno.EKEYREVOKED)
	ErrKeyRejected               = newLinuxOnly("key was rejected by service", errno.EKEYREJECTED)
	ErrRFKill                    = newLinuxOnly("operation not possible due to RF-kill", errno.ERFKILL)
	ErrHwPoison                  = newLinuxOnly("memory page has hardware error", errno.EHWPOISON)
)
