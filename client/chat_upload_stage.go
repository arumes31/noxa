// Upload failures are attributed to one pipeline stage so the frontend can
// explain reading, encrypting, transferring and publishing separately (502).
package main

import "errors"

type uploadStage string

const (
	uploadStageRead     uploadStage = "read"     // decoding the local file bytes
	uploadStageEncrypt  uploadStage = "encrypt"  // key generation and sealing
	uploadStageTransfer uploadStage = "transfer" // ciphertext upload to the server
	uploadStagePublish  uploadStage = "publish"  // sending the message that references the file
)

// uploadStagePrefix is the wire form the frontend parses out of the rejected
// promise text: "[upload:<stage>] <message>". upload-stage.js mirrors it.
const uploadStagePrefix = "[upload:"

type uploadStageError struct {
	Stage uploadStage
	Err   error
}

func (e *uploadStageError) Error() string {
	return uploadStagePrefix + string(e.Stage) + "] " + e.Err.Error()
}

func (e *uploadStageError) Unwrap() error { return e.Err }

// stageUploadError attributes err to stage unless it already carries one.
// Cancellation is never relabelled: the queue renders it as "Cancelled".
func stageUploadError(stage uploadStage, err error) error {
	if err == nil || errors.Is(err, errTransferCanceled) {
		return err
	}
	var staged *uploadStageError
	if errors.As(err, &staged) {
		return err
	}
	return &uploadStageError{Stage: stage, Err: err}
}

// uploadErrorStage reports the stage of err, or "" for unattributed errors.
func uploadErrorStage(err error) uploadStage {
	var staged *uploadStageError
	if errors.As(err, &staged) {
		return staged.Stage
	}
	return ""
}
