# VOICX original sound set

32 original dry contact sounds: fixed-filter noise impulses, muted taps and console clicks.
No oscillators, pitch sweeps, melodies, reverb or external samples. Mono PCM, 48 kHz/16 bit.
MIT licensed under the repository LICENSE. Regenerate: `node tools/generate-sounds.mjs`.

Generation validates finite PCM, duration, DC, endpoints, RMS and peak limits.
Metrics record measured whole-cue RMS (not integrated LUFS) and intended RMS.
PTT is quietest; ordinary messages/presence stay below direct attention and warnings.
Maximum asset peak: 0.115. Four voices at 200% sum to at most 0.92.

Human headphone/speaker listening is required to assess fatigue and semantic recognition;
waveform measurements and automated playback cannot certify those qualities.
