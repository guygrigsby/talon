import { t as definePluginEntry } from "../../plugin-entry-BpVWBiQw.js";
import { t as elevenLabsMediaUnderstandingProvider } from "../../media-understanding-provider-BGuh4PBT.js";
import { n as buildElevenLabsRealtimeTranscriptionProvider } from "../../realtime-transcription-provider-BwvXSWOM.js";
import { t as buildElevenLabsSpeechProvider } from "../../speech-provider-BG0nHV5c.js";
//#region extensions/elevenlabs/index.ts
var elevenlabs_default = definePluginEntry({
	id: "elevenlabs",
	name: "ElevenLabs Speech",
	description: "Bundled ElevenLabs speech provider",
	register(api) {
		api.registerSpeechProvider(buildElevenLabsSpeechProvider());
		api.registerMediaUnderstandingProvider(elevenLabsMediaUnderstandingProvider);
		api.registerRealtimeTranscriptionProvider(buildElevenLabsRealtimeTranscriptionProvider());
	}
});
//#endregion
export { elevenlabs_default as default };
