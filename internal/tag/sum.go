package tag

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
)

// Sum creates a checksum of the audio file data provided by the io.ReadSeeker which is metadata
// (ID3, MP4) invariant.
func Sum(r io.ReadSeeker) (string, error) {
	b, err := readBytes(r, 11)
	if err != nil {
		return "", err
	}

	_, err = r.Seek(-11, io.SeekCurrent)
	if err != nil {
		return "", fmt.Errorf("could not seek back to original position: %v", err)
	}

	switch {
	case string(b[0:4]) == "fLaC":
		return SumFLAC(r)

	case string(b[0:4]) == "OggS":
		return SumOGG(r)

	case string(b[4:8]) == "ftyp":
		return SumAtoms(r)

	case string(b[0:3]) == "ID3":
		return SumID3v2(r)

	case isASF(b):
		return SumASF(r)
	}

	h, err := SumID3v1(r)
	if err != nil {
		if err == ErrNotID3v1 {
			return SumAll(r)
		}
		return "", err
	}
	return h, nil
}

// SumAll returns a checksum of the content from the reader (until EOF).
func SumAll(r io.ReadSeeker) (string, error) {
	h := sha1.New()
	_, err := io.Copy(h, r)
	if err != nil {
		return "", nil
	}
	return hashSum(h), nil
}

// SumAtoms constructs a checksum of MP4 audio file data provided by the io.ReadSeeker which is
// metadata invariant.
func SumAtoms(r io.ReadSeeker) (string, error) {
	for {
		var size uint32
		err := binary.Read(r, binary.BigEndian, &size)
		if err != nil {
			if err == io.EOF {
				return "", fmt.Errorf("reached EOF before audio data")
			}
			return "", err
		}

		name, err := readString(r, 4)
		if err != nil {
			return "", err
		}

		// Length of the atom body: size 1 means a 64-bit size follows the
		// name, size 0 means the atom runs to the end of the file.
		n := int64(size) - 8
		toEOF := size == 0
		if size == 1 {
			var large uint64
			if err := binary.Read(r, binary.BigEndian, &large); err != nil {
				return "", err
			}
			n = int64(large) - 16
		}
		if n < 0 && !toEOF {
			return "", fmt.Errorf("invalid '%v' atom size", name)
		}

		switch name {
		case "meta":
			// next_item_id (int32)
			_, err := r.Seek(4, io.SeekCurrent)
			if err != nil {
				return "", err
			}
			fallthrough

		case "moov", "udta", "ilst":
			continue

		case "mdat": // stop when we get to the data
			h := sha1.New()
			if toEOF {
				_, err = io.Copy(h, r)
			} else {
				_, err = io.CopyN(h, r, n)
			}
			if err != nil {
				return "", fmt.Errorf("error reading audio data: %v", err)
			}
			return hashSum(h), nil
		}

		if toEOF {
			return "", fmt.Errorf("reached EOF before audio data")
		}
		_, err = r.Seek(n, io.SeekCurrent)
		if err != nil {
			return "", fmt.Errorf("error reading '%v' tag: %v", name, err)
		}
	}
}

// SumOGG constructs a checksum of the OGG audio file data provided by the io.ReadSeeker which is
// metadata invariant. It hashes every packet except the Vorbis comment and OpusTags headers, so
// the page layout (which changes when the comment grows or shrinks) does not matter.
func SumOGG(r io.ReadSeeker) (string, error) {
	h := sha1.New()
	od := &oggDemuxer{}
	for {
		packets, err := od.Read(r)
		if err == io.EOF {
			return hashSum(h), nil
		}
		if err != nil {
			return "", fmt.Errorf("error reading OGG page: %v", err)
		}

		for _, p := range packets {
			if bytes.HasPrefix(p, vorbisCommentPrefix) || bytes.HasPrefix(p, opusTagsPrefix) {
				continue
			}
			h.Write(p)
		}
	}
}

// sumMP3Audio hashes the bytes from start to the end of the file, leaving out a trailing ID3v1
// tag when there is one, so adding, editing or removing that tag keeps the hash.
func sumMP3Audio(r io.ReadSeeker, start int64) (string, error) {
	end, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return "", fmt.Errorf("error seeking to end of file: %v", err)
	}

	if end-start >= 128 {
		_, err = r.Seek(end-128, io.SeekStart)
		if err != nil {
			return "", fmt.Errorf("error seeking to ID3v1 tag: %v", err)
		}
		b, err := readBytes(r, 3)
		if err != nil {
			return "", fmt.Errorf("error reading ID3v1 tag: %v", err)
		}
		if string(b) == "TAG" {
			end -= 128
		}
	}

	if start > end {
		return "", fmt.Errorf("ID3v2 tag runs %d bytes past the end of the file", start-end)
	}

	_, err = r.Seek(start, io.SeekStart)
	if err != nil {
		return "", fmt.Errorf("error seeking to audio data: %v", err)
	}

	h := sha1.New()
	_, err = io.CopyN(h, r, end-start)
	if err != nil {
		return "", fmt.Errorf("error reading %v bytes: %v", end-start, err)
	}
	return hashSum(h), nil
}

// SumID3v1 constructs a checksum of MP3 audio file data (which may end with an ID3v1 tag) provided
// by the io.ReadSeeker which is metadata invariant.
func SumID3v1(r io.ReadSeeker) (string, error) {
	start, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return "", fmt.Errorf("error reading current position: %v", err)
	}
	return sumMP3Audio(r, start)
}

// SumID3v2 constructs a checksum of MP3 audio file data (assumed to start with an ID3v2 tag)
// provided by the io.ReadSeeker which is metadata invariant. It skips the whole tag: the 10-byte
// header, the synchsafe size (which covers any extended header and padding) and the 10-byte
// footer that a v2.4 tag carries when flag 0x10 is set.
func SumID3v2(r io.ReadSeeker) (string, error) {
	start, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return "", fmt.Errorf("error reading current position: %v", err)
	}

	b, err := readBytes(r, 10)
	if err != nil {
		return "", fmt.Errorf("error reading ID3v2 header: %v", err)
	}
	if string(b[0:3]) != "ID3" {
		return "", fmt.Errorf("expected to read \"ID3\"")
	}

	size := 10 + int64(get7BitChunkedInt(b[6:10]))
	if b[3] == 4 && getBit(b[5], 4) {
		size += 10
	}
	return sumMP3Audio(r, start+size)
}

// SumFLAC costructs a checksum of the FLAC audio file data provided by the io.ReadSeeker (ignores
// metadata fields).
func SumFLAC(r io.ReadSeeker) (string, error) {
	flac, err := readString(r, 4)
	if err != nil {
		return "", err
	}
	if flac != "fLaC" {
		return "", errors.New("expected 'fLaC'")
	}

	for {
		last, err := skipFLACMetadataBlock(r)
		if err != nil {
			return "", err
		}

		if last {
			break
		}
	}

	h := sha1.New()
	_, err = io.Copy(h, r)
	if err != nil {
		return "", fmt.Errorf("error reading data bytes from FLAC: %v", err)
	}
	return hashSum(h), nil
}

func skipFLACMetadataBlock(r io.ReadSeeker) (last bool, err error) {
	blockHeader, err := readBytes(r, 1)
	if err != nil {
		return
	}

	if getBit(blockHeader[0], 7) {
		blockHeader[0] ^= (1 << 7)
		last = true
	}

	blockLen, err := readInt(r, 3)
	if err != nil {
		return
	}

	_, err = r.Seek(int64(blockLen), io.SeekCurrent)
	return
}

func hashSum(h hash.Hash) string {
	return fmt.Sprintf("%x", h.Sum([]byte{}))
}
