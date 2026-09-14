package main

import (
	"debug/pe"
	"encoding/binary"
	"fmt"
	"os"
)

// validateClientExecutable checks the platform promised by clientAssetName.
// Authentication proves who published the bytes, not that Windows can load
// them. Check file-backed sections too: debug/pe parses headers lazily and can
// otherwise accept an image whose section data was truncated.
func validateClientExecutable(f *os.File) error {
	var dosHeader [64]byte
	if _, err := f.ReadAt(dosHeader[:], 0); err != nil {
		return fmt.Errorf("read DOS header: %w", err)
	}
	if dosHeader[0] != 'M' || dosHeader[1] != 'Z' {
		return fmt.Errorf("missing DOS executable header")
	}
	peOffset := uint64(binary.LittleEndian.Uint32(dosHeader[0x3c:]))
	if peOffset < uint64(len(dosHeader)) {
		return fmt.Errorf("PE header overlaps DOS header")
	}
	image, err := pe.NewFile(f)
	if err != nil {
		return fmt.Errorf("read PE headers: %w", err)
	}
	defer func() { _ = image.Close() }()
	if image.Machine != pe.IMAGE_FILE_MACHINE_AMD64 ||
		image.Characteristics&pe.IMAGE_FILE_EXECUTABLE_IMAGE == 0 ||
		image.Characteristics&pe.IMAGE_FILE_DLL != 0 {
		return fmt.Errorf("expected an AMD64 executable image, not a DLL")
	}
	optional, ok := image.OptionalHeader.(*pe.OptionalHeader64)
	if !ok || optional.Magic != 0x20b {
		return fmt.Errorf("expected PE32+ optional header")
	}
	if optional.Subsystem != pe.IMAGE_SUBSYSTEM_WINDOWS_GUI && optional.Subsystem != pe.IMAGE_SUBSYSTEM_WINDOWS_CUI {
		return fmt.Errorf("expected a Windows GUI or console executable")
	}
	fileAlignment := optional.FileAlignment
	sectionAlignment := optional.SectionAlignment
	if fileAlignment < 512 || fileAlignment > 65536 || fileAlignment&(fileAlignment-1) != 0 {
		return fmt.Errorf("invalid file alignment")
	}
	// AMD64 pages are 4 KiB. Subpage images require identical alignments.
	if sectionAlignment < fileAlignment || sectionAlignment < 4096 && sectionAlignment != fileAlignment {
		return fmt.Errorf("invalid section alignment")
	}
	// DOS stub, PE signature, COFF header, optional header, and section table
	// must all fit inside the declared (file-aligned) headers. Use wide sums
	// because the PE offset itself is a uint32 read from the downloaded file.
	headerEnd := peOffset + 4 + 20 + uint64(image.SizeOfOptionalHeader) + uint64(image.NumberOfSections)*40
	if uint64(optional.SizeOfHeaders) < headerEnd || optional.SizeOfHeaders%fileAlignment != 0 {
		return fmt.Errorf("declared headers do not cover the aligned header table")
	}
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat executable: %w", err)
	}
	if optional.SizeOfHeaders == 0 || int64(optional.SizeOfHeaders) > info.Size() {
		return fmt.Errorf("truncated executable headers")
	}
	entryBacked := false
	for _, section := range image.Sections {
		if section.Size == 0 {
			continue // uninitialized data does not occupy file bytes
		}
		if section.Offset < optional.SizeOfHeaders || int64(section.Offset)+int64(section.Size) > info.Size() {
			return fmt.Errorf("invalid or truncated section %q", section.Name)
		}
		entry := uint64(optional.AddressOfEntryPoint)
		start := uint64(section.VirtualAddress)
		if entry >= start && entry-start < uint64(section.Size) &&
			entry < uint64(optional.SizeOfImage) && section.Characteristics&pe.IMAGE_SCN_MEM_EXECUTE != 0 {
			entryBacked = true
		}
	}
	if optional.AddressOfEntryPoint == 0 || !entryBacked {
		return fmt.Errorf("entry point is not backed by executable section data")
	}
	return nil
}
