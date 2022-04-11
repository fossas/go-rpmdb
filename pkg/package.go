package rpmdb

import (
	"bytes"
	"encoding/binary"

	"strings"

	"golang.org/x/xerrors"
)

type PackageInfo struct {
	Epoch           *int
	Name            string
	Version         string
	Release         string
	Arch            string
	SourceRpm       string
	Size            int
	License         string
	Vendor          string
	DigestAlgorithm DigestAlgorithm
	Files           []FileInfo
}

type FileInfo struct {
	Path      string
	Mode      uint16
	Digest    string
	Size      int32
	Username  string
	Groupname string
	Flags     FileFlags
}

const (
	sizeOfInt32  = 4
	sizeOfUInt16 = 2
)

func parseInt32(data []byte) (int, error) {
	var value int32
	reader := bytes.NewReader(data)
	if err := binary.Read(reader, binary.BigEndian, &value); err != nil {
		return 0, xerrors.Errorf("failed to read binary: %w", err)
	}
	return int(value), nil
}

func parseStringArray(data []byte) []string {
	elements := strings.Split(string(data), "\x00")
	if len(elements) > 0 && elements[len(elements)-1] == "" {
		return elements[:len(elements)-1]
	}
	return elements
}

func parseInt32Array(data []byte, arraySize int) ([]int32, error) {
	var length = arraySize / sizeOfInt32
	values := make([]int32, length)
	reader := bytes.NewReader(data)
	if err := binary.Read(reader, binary.BigEndian, &values); err != nil {
		return nil, xerrors.Errorf("failed to read binary: %w", err)
	}
	return values, nil
}

func parseUInt16Array(data []byte, arraySize int) ([]uint16, error) {
	var length = arraySize / sizeOfUInt16
	values := make([]uint16, length)
	reader := bytes.NewReader(data)
	if err := binary.Read(reader, binary.BigEndian, &values); err != nil {
		return nil, xerrors.Errorf("failed to read binary: %w", err)
	}
	return values, nil
}

// ref. https://github.com/rpm-software-management/rpm/blob/rpm-4.14.3-release/lib/tagexts.c#L752
func getNEVRA(indexEntries []indexEntry) (*PackageInfo, error) {
	pkgInfo := &PackageInfo{}
	for _, ie := range indexEntries {
		switch ie.Info.Tag {
		case RPMTAG_NAME:
			if ie.Info.Type != RPM_STRING_TYPE {
				return nil, xerrors.New("invalid tag name")
			}
			pkgInfo.Name = string(bytes.TrimRight(ie.Data, "\x00"))
		case RPMTAG_EPOCH:
			if ie.Info.Type != RPM_INT32_TYPE {
				return nil, xerrors.New("invalid tag epoch")
			}
			if ie.Data != nil {
				value, err := parseInt32(ie.Data)
				if err != nil {
					return nil, xerrors.Errorf("failed to parse epoch: %w", err)
				}
				pkgInfo.Epoch = &value
			}
		case RPMTAG_VERSION:
			if ie.Info.Type != RPM_STRING_TYPE {
				return nil, xerrors.New("invalid tag version")
			}
			pkgInfo.Version = string(bytes.TrimRight(ie.Data, "\x00"))
		case RPMTAG_RELEASE:
			if ie.Info.Type != RPM_STRING_TYPE {
				return nil, xerrors.New("invalid tag release")
			}
			pkgInfo.Release = string(bytes.TrimRight(ie.Data, "\x00"))
		case RPMTAG_ARCH:
			if ie.Info.Type != RPM_STRING_TYPE {
				return nil, xerrors.New("invalid tag arch")
			}
			pkgInfo.Arch = string(bytes.TrimRight(ie.Data, "\x00"))
		case RPMTAG_SOURCERPM:
			if ie.Info.Type != RPM_STRING_TYPE {
				return nil, xerrors.New("invalid tag sourcerpm")
			}
			pkgInfo.SourceRpm = string(bytes.TrimRight(ie.Data, "\x00"))
			if pkgInfo.SourceRpm == "(none)" {
				pkgInfo.SourceRpm = ""
			}
		case RPMTAG_LICENSE:
			if ie.Info.Type != RPM_STRING_TYPE {
				return nil, xerrors.New("invalid tag license")
			}
			pkgInfo.License = string(bytes.TrimRight(ie.Data, "\x00"))
			if pkgInfo.License == "(none)" {
				pkgInfo.License = ""
			}
		case RPMTAG_VENDOR:
			if ie.Info.Type != RPM_STRING_TYPE {
				return nil, xerrors.New("invalid tag vendor")
			}
			pkgInfo.Vendor = string(bytes.TrimRight(ie.Data, "\x00"))
			if pkgInfo.Vendor == "(none)" {
				pkgInfo.Vendor = ""
			}
		case RPMTAG_SIZE:
			if ie.Info.Type != RPM_INT32_TYPE {
				return nil, xerrors.New("invalid tag size")
			}

			var size int32
			reader := bytes.NewReader(ie.Data)
			if err := binary.Read(reader, binary.BigEndian, &size); err != nil {
				return nil, xerrors.Errorf("failed to read binary (size): %w", err)
			}
			pkgInfo.Size = int(size)
		case RPMTAG_FILEDIGESTALGO:
			// note: all digests within a package entry only supports a single digest algorithm (there may be future support for
			// algorithm noted for each file entry, but currently unimplemented: https://github.com/rpm-software-management/rpm/blob/0b75075a8d006c8f792d33a57eae7da6b66a4591/lib/rpmtag.h#L256)
			if ie.Info.Type != RPM_INT32_TYPE {
				return nil, xerrors.New("invalid tag digest algo")
			}

			var digestAlgo int32
			reader := bytes.NewReader(ie.Data)
			if err := binary.Read(reader, binary.BigEndian, &digestAlgo); err != nil {
				return nil, xerrors.Errorf("failed to read binary digest algo: %w", err)
			}
			pkgInfo.DigestAlgorithm = DigestAlgorithm(int(digestAlgo))
		}

	}

	files, err := getFileInfo(indexEntries)
	if err != nil {
		return nil, xerrors.Errorf("failed to read package files: %w", err)
	}

	pkgInfo.Files = files

	return pkgInfo, nil
}

func getFileInfo(indexEntries []indexEntry) ([]FileInfo, error) {
	var err error

	// each of these fields are arrays of metadata for a single file, where the same index across variables are
	// for the same file (this is how the information is stored within the RPM DB)
	var allBasenames []string
	var allDirs []string
	var allDirIndexes []int32
	var allFileDigests []string
	var allFileModes []uint16
	var allFileSizes []int32
	var allFileFlags []int32
	var allUserNames []string
	var allGroupNames []string

	for _, indexEntry := range indexEntries {
		switch indexEntry.Info.Tag {

		case RPMTAG_FILESIZES:
			// note: there is no distinction between int32, uint32, and []uint32
			if indexEntry.Info.Type != RPM_INT32_TYPE {
				return nil, xerrors.New("invalid tag file-sizes")
			}
			allFileSizes, err = parseInt32Array(indexEntry.Data, indexEntry.Length)
			if err != nil {
				return nil, xerrors.Errorf("failed to parse file-sizes: %w", err)
			}
		case RPMTAG_FILEFLAGS:
			// note: there is no distinction between int32, uint32, and []uint32
			if indexEntry.Info.Type != RPM_INT32_TYPE {
				return nil, xerrors.New("invalid tag file-flags")
			}
			allFileFlags, err = parseInt32Array(indexEntry.Data, indexEntry.Length)
			if err != nil {
				return nil, xerrors.Errorf("failed to parse file-flags: %w", err)
			}
		case RPMTAG_FILEDIGESTS:
			if indexEntry.Info.Type != RPM_STRING_ARRAY_TYPE {
				return nil, xerrors.New("invalid tag file-digests")
			}
			allFileDigests = parseStringArray(indexEntry.Data)
		case RPMTAG_FILEMODES:
			// note: there is no distinction between int16, uint16, and []uint16
			if indexEntry.Info.Type != RPM_INT16_TYPE {
				return nil, xerrors.New("invalid tag file-modes")
			}
			allFileModes, err = parseUInt16Array(indexEntry.Data, indexEntry.Length)
			if err != nil {
				return nil, xerrors.Errorf("failed to parse file-modes: %w", err)
			}
		case RPMTAG_BASENAMES:
			if indexEntry.Info.Type != RPM_STRING_ARRAY_TYPE {
				return nil, xerrors.New("invalid tag basenames")
			}
			allBasenames = parseStringArray(indexEntry.Data)
		case RPMTAG_FILEUSERNAME:
			if indexEntry.Info.Type != RPM_STRING_ARRAY_TYPE {
				return nil, xerrors.New("invalid tag usernames")
			}
			allUserNames = parseStringArray(indexEntry.Data)
		case RPMTAG_FILEGROUPNAME:
			if indexEntry.Info.Type != RPM_STRING_ARRAY_TYPE {
				return nil, xerrors.New("invalid tag groupnames")
			}
			allGroupNames = parseStringArray(indexEntry.Data)
		case RPMTAG_DIRNAMES:
			if indexEntry.Info.Type != RPM_STRING_ARRAY_TYPE {
				return nil, xerrors.New("invalid tag dir-names")
			}
			allDirs = parseStringArray(indexEntry.Data)
		case RPMTAG_DIRINDEXES:
			// note: there is no distinction between int32, uint32, and []uint32
			if indexEntry.Info.Type != RPM_INT32_TYPE {
				return nil, xerrors.New("invalid tag dir-indexes")
			}
			allDirIndexes, err = parseInt32Array(indexEntry.Data, indexEntry.Length)
			if err != nil {
				return nil, xerrors.Errorf("failed to parse dir-indexes: %w", err)
			}
		}
	}

	// now that we have all of the available metadata, piece together a list of files and their metadata
	var files []FileInfo
	if allDirs != nil && allDirIndexes != nil {
		for i, file := range allBasenames {
			var digest, username, groupname string
			var mode uint16
			var size, flags int32

			if allFileDigests != nil && len(allFileDigests) > i {
				digest = allFileDigests[i]
			}

			if allFileModes != nil && len(allFileModes) > i {
				mode = allFileModes[i]
			}

			if allFileSizes != nil && len(allFileSizes) > i {
				size = allFileSizes[i]
			}

			if allUserNames != nil && len(allUserNames) > i {
				username = allUserNames[i]
			}

			if allGroupNames != nil && len(allGroupNames) > i {
				groupname = allGroupNames[i]
			}

			if allFileFlags != nil && len(allFileFlags) > i {
				flags = allFileFlags[i]
			}

			record := FileInfo{
				Path:      allDirs[allDirIndexes[i]] + file,
				Mode:      mode,
				Digest:    digest,
				Size:      size,
				Username:  username,
				Groupname: groupname,
				Flags:     FileFlags(flags),
			}
			files = append(files, record)
		}
	}

	return files, nil
}
