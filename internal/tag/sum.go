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

	case string(b[0:4]) == "DSD ":
		return SumDSF(r)

	case isASF(b):
		return SumASF(r)

	case isWAV(b):
		return SumWAV(r)
	}

	return SumID3v1(r)
}

// SumAll returns a checksum of the content from the reader (until EOF).
func SumAll(r io.ReadSeeker) (string, error) {
	h := sha1.New()
	_, err := io.Copy(h, r)
	if err != nil {
		return "", err
	}
	return hashSum(h), nil
}

// SumAtoms constructs a checksum of MP4 audio file data provided by the io.ReadSeeker which is
// metadata invariant: the payload of every mdat atom, in file order. A fragmented MP4 has one
// mdat for each fragment, and the first alone is only the first seconds of the audio.
func SumAtoms(r io.ReadSeeker) (string, error) {
	h := sha1.New()
	found := false // an mdat has been hashed

	// stop ends the walk at an atom that cannot be read: with the audio
	// hashed that is the end of the atoms, without it an error.
	stop := func(err error) (string, error) {
		if found {
			return hashSum(h), nil
		}
		return "", err
	}

	for {
		var size uint32
		err := binary.Read(r, binary.BigEndian, &size)
		if err != nil {
			if err == io.EOF {
				err = fmt.Errorf("reached EOF before audio data")
			}
			return stop(err)
		}

		name, err := readString(r, 4)
		if err != nil {
			return stop(err)
		}

		// Length of the atom body: size 1 means a 64-bit size follows the
		// name, size 0 means the atom runs to the end of the file.
		n := int64(size) - 8
		toEOF := size == 0
		if size == 1 {
			var large uint64
			if err := binary.Read(r, binary.BigEndian, &large); err != nil {
				return stop(err)
			}
			n = int64(large) - 16
		}
		if n < 0 && !toEOF {
			return stop(fmt.Errorf("invalid '%v' atom size", name))
		}

		switch name {
		case "meta":
			// next_item_id (int32)
			_, err := r.Seek(4, io.SeekCurrent)
			if err != nil {
				return stop(err)
			}
			fallthrough

		case "moov", "udta", "ilst":
			continue

		case "mdat":
			if toEOF {
				_, err = io.Copy(h, r)
			} else {
				_, err = io.CopyN(h, r, n)
			}
			if err != nil {
				return "", fmt.Errorf("error reading audio data: %v", err)
			}
			found = true
			continue
		}

		if toEOF {
			return stop(fmt.Errorf("reached EOF before audio data"))
		}
		_, err = r.Seek(n, io.SeekCurrent)
		if err != nil {
			return stop(fmt.Errorf("error reading '%v' tag: %v", name, err))
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

// sumAudio hashes the bytes from start to the end of the file, leaving out the trailing APEv2
// and ID3v1 tags (in either order, and ID3v1 on both sides of APEv2), so adding, editing or
// removing those tags keeps the hash. MP3, APE, MPC and AAC files are hashed this way.
func sumAudio(r io.ReadSeeker, start int64) (string, error) {
	end, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return "", fmt.Errorf("error seeking to end of file: %v", err)
	}

	if start > end {
		return "", fmt.Errorf("ID3v2 tag runs %d bytes past the end of the file", start-end)
	}

	t, err := findTrailingTags(r, start, end)
	if err != nil {
		return "", err
	}
	end = t.audioEnd

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

// SumID3v1 constructs a checksum of MP3, APE, MPC or AAC audio file data (which may end with
// APEv2 and ID3v1 tags) provided by the io.ReadSeeker which is metadata invariant.
func SumID3v1(r io.ReadSeeker) (string, error) {
	start, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return "", fmt.Errorf("error reading current position: %v", err)
	}
	return sumAudio(r, start)
}

// SumID3v2 constructs a checksum of MP3, APE, MPC or AAC audio file data (assumed to start with
// an ID3v2 tag) provided by the io.ReadSeeker which is metadata invariant. It skips the whole
// tag: the 10-byte header, the synchsafe size (which covers any extended header and padding) and
// the 10-byte footer that a v2.4 tag carries when flag 0x10 is set.
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
	audioStart := start + id3v2TagSize(b)

	// A FLAC stream behind the tag is hashed as FLAC, without its metadata
	// blocks, so its hash is that of the same stream with no ID3v2 tag.
	if head, err := readAt(r, audioStart, 4); err == nil && string(head) == "fLaC" {
		if _, err := r.Seek(audioStart, io.SeekStart); err != nil {
			return "", fmt.Errorf("error seeking to FLAC stream: %v", err)
		}
		return SumFLAC(r)
	}
	return sumAudio(r, audioStart)
}

// SumDSF constructs a checksum of the DSF audio file data provided by the io.ReadSeeker which is
// metadata invariant: the payload of the data chunk. The ID3v2 tag at the end of the file is left
// out, and so is the DSD chunk, which holds the file's size and where the tag is.
func SumDSF(r io.ReadSeeker) (string, error) {
	off, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return "", fmt.Errorf("error reading current position: %v", err)
	}

	// Each chunk starts with its ID and its size, which counts those 12 bytes.
	for {
		if _, err := r.Seek(off, io.SeekStart); err != nil {
			return "", fmt.Errorf("error seeking to DSF chunk: %v", err)
		}
		b, err := readBytes(r, 12)
		if err != nil {
			return "", fmt.Errorf("error reading DSF chunk header: %v", err)
		}
		size := binary.LittleEndian.Uint64(b[4:12])
		if size < 12 || size > 1<<62 {
			return "", fmt.Errorf("invalid DSF %q chunk size %d", b[0:4], size)
		}
		if string(b[0:4]) == "data" {
			h := sha1.New()
			if _, err := io.CopyN(h, r, int64(size-12)); err != nil {
				return "", fmt.Errorf("error reading audio data: %v", err)
			}
			return hashSum(h), nil
		}
		off += int64(size)
	}
}

// SumWAV constructs a checksum of the payload of the data chunk of the WAV file provided by the
// io.ReadSeeker, which is metadata invariant: tags live in other chunks, which taggers add, drop
// and move around. The pad byte after an odd-sized payload is not hashed. A data chunk whose size
// is unknown (0 or 0xFFFFFFFF, as streamed WAVs leave it) or runs past the end of the file is
// hashed to the end of the file.
func SumWAV(r io.ReadSeeker) (string, error) {
	start, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return "", fmt.Errorf("error reading current position: %v", err)
	}
	end, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return "", fmt.Errorf("error seeking to end of file: %v", err)
	}

	var data *riffChunk
	err = walkRIFF(r, start, end, func(c riffChunk) (bool, error) {
		if c.id == "data" {
			data = &c
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return "", err
	}
	if data == nil {
		return "", errors.New("WAV file has no data chunk")
	}

	n := data.size
	if n == 0 || n == 0xFFFFFFFF || data.data+n > end {
		n = end - data.data
	}
	if _, err := r.Seek(data.data, io.SeekStart); err != nil {
		return "", fmt.Errorf("error seeking to audio data: %v", err)
	}
	h := sha1.New()
	if _, err := io.CopyN(h, r, n); err != nil {
		return "", fmt.Errorf("error reading audio data: %v", err)
	}
	return hashSum(h), nil
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
