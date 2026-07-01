package stt

func CloneTranscriptionRequest(req TranscriptionRequest) TranscriptionRequest {
	if req.Audio != nil {
		req.Audio = append([]byte(nil), req.Audio...)
	}
	return req
}
