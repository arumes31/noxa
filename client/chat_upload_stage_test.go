package main

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestUploadStageAttribution(t *testing.T) {
	ok := func(string, []byte) error { return nil }
	if _, err := uploadChatAttachmentUsing("a.txt", "%%%not-base64%%%", ok); uploadErrorStage(err) != uploadStageRead || !strings.HasPrefix(err.Error(), "[upload:read] ") {
		t.Fatalf("read stage: %v", err)
	}
	data := base64.StdEncoding.EncodeToString([]byte("hello"))
	failing := errors.New("server refused")
	_, err := uploadChatAttachmentUsing("a.txt", data, func(string, []byte) error { return failing })
	if uploadErrorStage(err) != uploadStageTransfer || !errors.Is(err, failing) || err.Error() != "[upload:transfer] server refused" {
		t.Fatalf("transfer stage: %v", err)
	}
	// Cancellation keeps its sentinel so callers still match errTransferCanceled.
	_, err = uploadChatAttachmentUsing("a.txt", data, func(string, []byte) error { return errTransferCanceled })
	if !errors.Is(err, errTransferCanceled) || uploadErrorStage(err) != "" {
		t.Fatalf("canceled: %v", err)
	}
	// An already attributed error is never relabelled.
	wrapped := stageUploadError(uploadStageTransfer, stageUploadError(uploadStageEncrypt, errors.New("seal")))
	if uploadErrorStage(wrapped) != uploadStageEncrypt {
		t.Fatalf("relabelled: %v", wrapped)
	}
	if token, err := uploadChatAttachmentUsing("a.txt", data, ok); err != nil || !strings.HasPrefix(token, "[file:") {
		t.Fatalf("success: %q %v", token, err)
	}
}
