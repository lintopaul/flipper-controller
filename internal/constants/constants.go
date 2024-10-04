package constants

import "time"

const (
	ReasonRolloutRestartFailed      = "RolloutRestartFailed"
	ReasonRolloutRestartTriggered   = "RolloutRestartTriggered"
	ReasonRolloutRestartUnsupported = "RolloutRestartUnsupported"
	ReasonAnnotationSucceeded       = "AnnotationAdditionSucceeded"
	ReasonAnnotationFailed          = "AnnotationAdditionFailed"
)
const (
	FlipperInterval = time.Duration(10 * time.Minute)
	RequeueInterval = time.Duration(10 * time.Second)
)

const (
	AnnotationFlipperRestartedAt = "apps.flipper.io/restartedAt"
	RolloutRestartAnnotation     = "kubectl.kubernetes.io/restartedAt"
	RolloutManagedBy             = "apps.flipper.io/managedBy"
	rolloutIntervalGroupName     = "apps.flipper.io/IntervalGroup"
)

const (
	ErrorUnsupportedKind = "unsupported Kind %v"
)
