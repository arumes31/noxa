package main

import "os"

// zoneIdentifier is the Mark-of-the-Web: ZoneId=3 is the Internet zone, so
// SmartScreen and Office treat a received file as downloaded.
const zoneIdentifier = "[ZoneTransfer]\r\nZoneId=3\r\n"

// markDownloaded tags a saved download with the Mark-of-the-Web. Best effort:
// volumes without alternate data streams (FAT, exFAT, network shares) refuse it.
func markDownloaded(path string) {
	_ = os.WriteFile(path+":Zone.Identifier", []byte(zoneIdentifier), 0o600)
}
