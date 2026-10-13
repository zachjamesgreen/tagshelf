// fp computes the AcoustID fingerprint of an audio file the way AcoustID's
// own fpcalc does: the first 120 seconds, decoded by FFmpeg, converted to
// the rate and channels Chromaprint works in with the resampler settings of
// fpcalc, and given to Chromaprint.
//
// Usage: fp FILE [SECONDS]
//
// It prints DURATION=<whole seconds> and FINGERPRINT=<text>, as fpcalc
// does, and leaves with 0. When it cannot, it prints ERROR: and why to the
// standard error, and leaves with 2.
//
// It is built into fp.wasm by build.sh, and run by tagshelf.
#include <cstdio>
#include <cstdlib>
#include <cstring>

extern "C" {
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/channel_layout.h>
#include <libavutil/opt.h>
#include <libswresample/swresample.h>
}
#include <chromaprint.h>

static int fail(const char *what, int err) {
	char buf[AV_ERROR_MAX_STRING_SIZE] = "";
	if (err < 0) av_strerror(err, buf, sizeof buf);
	fprintf(stderr, "ERROR: %s%s%s\n", what, err < 0 ? ": " : "", buf);
	return 2;
}

int main(int argc, char **argv) {
	if (argc < 2) return fail("no input file", 0);
	double max_duration = argc > 2 ? atof(argv[2]) : 120;
	av_log_set_level(AV_LOG_QUIET);

	AVFormatContext *fmt = nullptr;
	int ret = avformat_open_input(&fmt, argv[1], nullptr, nullptr);
	if (ret < 0) return fail("could not open the file", ret);
	ret = avformat_find_stream_info(fmt, nullptr);
	if (ret < 0) return fail("could not find what the file holds", ret);
	const AVCodec *codec = nullptr;
	int index = av_find_best_stream(fmt, AVMEDIA_TYPE_AUDIO, -1, -1, &codec, 0);
	if (index < 0) return fail("could not find audio it can decode in the file", index);
	AVStream *stream = fmt->streams[index];

	AVCodecContext *dec = avcodec_alloc_context3(codec);
	if (!dec) return fail("out of memory", 0);
	ret = avcodec_parameters_to_context(dec, stream->codecpar);
	if (ret < 0) return fail("could not read how the audio is encoded", ret);
	dec->pkt_timebase = stream->time_base;
	dec->request_sample_fmt = AV_SAMPLE_FMT_S16;
	ret = avcodec_open2(dec, codec, nullptr);
	if (ret < 0) return fail("could not start the decoder", ret);
	if (dec->sample_rate <= 0 || dec->ch_layout.nb_channels <= 0) return fail("the audio has no sample rate or no channels", 0);
	if (dec->ch_layout.order == AV_CHANNEL_ORDER_UNSPEC) {
		int n = dec->ch_layout.nb_channels;
		av_channel_layout_uninit(&dec->ch_layout);
		av_channel_layout_default(&dec->ch_layout, n);
	}

	ChromaprintContext *ctx = chromaprint_new(CHROMAPRINT_ALGORITHM_DEFAULT);
	if (!ctx) return fail("out of memory", 0);
	const int rate = chromaprint_get_sample_rate(ctx);
	const int channels = chromaprint_get_num_channels(ctx);

	AVChannelLayout out_layout;
	av_channel_layout_default(&out_layout, channels);
	SwrContext *swr = nullptr;
	ret = swr_alloc_set_opts2(&swr, &out_layout, AV_SAMPLE_FMT_S16, rate, &dec->ch_layout, dec->sample_fmt, dec->sample_rate, 0, nullptr);
	if (ret < 0) return fail("could not make the audio converter", ret);
	// What fpcalc calls its compatible mode.
	av_opt_set_int(swr, "resampler", SWR_ENGINE_SWR, 0);
	av_opt_set_int(swr, "filter_size", 16, 0);
	av_opt_set_int(swr, "phase_shift", 8, 0);
	av_opt_set_int(swr, "linear_interp", 1, 0);
	av_opt_set_double(swr, "cutoff", 0.8, 0);
	ret = swr_init(swr);
	if (ret < 0) return fail("could not start the audio converter", ret);

	if (!chromaprint_start(ctx, rate, channels)) return fail("could not start the fingerprint", 0);
	const size_t limit = (size_t)(max_duration * rate);
	size_t fed = 0;
	bool done = false;

	AVPacket *pkt = av_packet_alloc();
	AVFrame *frame = av_frame_alloc();
	if (!pkt || !frame) return fail("out of memory", 0);
	uint8_t *buf[1] = {nullptr};
	int buf_samples = 0;
	auto feed = [&](const int16_t *data, size_t n) -> bool {
		if (limit > 0 && n > limit - fed) {
			n = limit - fed;
			done = true;
		}
		if (n > 0 && !chromaprint_feed(ctx, data, (int)(n * channels))) return false;
		fed += n;
		return true;
	};
	// convert gives a decoded frame to Chromaprint, or with no frame what the
	// converter still holds.
	auto convert = [&](AVFrame *f) -> int {
		int want = f ? (int)av_rescale_rnd(swr_get_delay(swr, dec->sample_rate) + f->nb_samples, rate, dec->sample_rate, AV_ROUND_UP) + 32
		             : (int)swr_get_delay(swr, rate) + 32;
		if (want > buf_samples) {
			av_freep(&buf[0]);
			buf_samples = want > 8192 ? want : 8192;
			int linesize;
			int r = av_samples_alloc(buf, &linesize, channels, buf_samples, AV_SAMPLE_FMT_S16, 1);
			if (r < 0) return r;
		}
		int n = swr_convert(swr, buf, buf_samples, f ? (const uint8_t **)f->extended_data : nullptr, f ? f->nb_samples : 0);
		if (n < 0) return n;
		return feed((const int16_t *)buf[0], (size_t)n) ? 0 : AVERROR_UNKNOWN;
	};
	auto drain = [&]() -> int {
		while (!done) {
			int r = avcodec_receive_frame(dec, frame);
			if (r == AVERROR(EAGAIN) || r == AVERROR_EOF) return 0;
			if (r < 0) return r;
			r = convert(frame);
			av_frame_unref(frame);
			if (r < 0) return r;
		}
		return 0;
	};

	int bad_packets = 0;
	while (!done) {
		ret = av_read_frame(fmt, pkt);
		if (ret == AVERROR_EOF) break;
		if (ret < 0) return fail("could not read the file", ret);
		if (pkt->stream_index == index) {
			ret = avcodec_send_packet(dec, pkt);
			if (ret < 0 && ret != AVERROR(EAGAIN)) {
				// One bad packet is passed over, as fpcalc does; two in a row end it.
				if (++bad_packets > 1) return fail("could not decode the audio", ret);
			} else {
				bad_packets = 0;
			}
			ret = drain();
			if (ret < 0) return fail("could not decode the audio", ret);
		}
		av_packet_unref(pkt);
	}
	if (!done) {
		avcodec_send_packet(dec, nullptr);
		ret = drain();
		if (ret < 0) return fail("could not decode the audio", ret);
		if (!done) {
			ret = convert(nullptr);
			if (ret < 0) return fail("could not convert the audio", ret);
		}
	}
	if (fed == 0) return fail("the file holds no audio to listen to", 0);
	if (!chromaprint_finish(ctx)) return fail("could not finish the fingerprint", 0);
	// A few seconds of audio make a fingerprint of nothing, which AcoustID
	// would only refuse.
	int items = 0;
	if (!chromaprint_get_raw_fingerprint_size(ctx, &items) || items == 0) return fail("the audio is too short to be told by its sound", 0);

	double duration = (double)fed / rate;
	if (stream->duration != AV_NOPTS_VALUE) {
		duration = (double)(1000 * stream->time_base.num * stream->duration / stream->time_base.den) / 1000.0;
	} else if (fmt->duration != AV_NOPTS_VALUE) {
		duration = (double)(1000 * fmt->duration / AV_TIME_BASE) / 1000.0;
	}
	char *fp = nullptr;
	if (!chromaprint_get_fingerprint(ctx, &fp) || !fp || !*fp) return fail("the audio gave no fingerprint", 0);
	printf("DURATION=%d\nFINGERPRINT=%s\n", (int)duration, fp);
	return 0;
}
