package store

import (
	"sort"
	"sync"
	"time"
)

type JobStatus string

const (
	StatusUploading   JobStatus = "uploading"
	StatusUploaded    JobStatus = "uploaded"
	StatusTranscoding JobStatus = "transcoding"
	StatusReady       JobStatus = "ready"
	StatusFinished    JobStatus = "finished"
	StatusFailed      JobStatus = "failed"
)

type Job struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	Status    JobStatus `json:"status"`
	Error     string    `json:"error,omitempty"`

	UploadPath    string `json:"-"`
	HLSDir        string `json:"-"`
	PlaylistPath  string `json:"-"`
	PlaylistURL   string `json:"playlistUrl,omitempty"`
	FFmpegPID     int    `json:"ffmpegPid,omitempty"`
	FFmpegLogPath string `json:"ffmpegLogPath,omitempty"`
}

type Store struct {
	mu   sync.RWMutex
	jobs map[string]*Job
}

func New() *Store {
	return &Store{jobs: make(map[string]*Job)}
}

func (s *Store) Put(j *Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[j.ID] = j
}

func (s *Store) Get(id string) (*Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	return j, ok
}

func (s *Store) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, id)
}

func (s *Store) List() []*Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, j)
	}
	return out
}

func (s *Store) Update(id string, f func(*Job)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return
	}
	f(j)
}

func (s *Store) SetStatus(id string, st JobStatus, errMsg string) {
	s.Update(id, func(j *Job) {
		j.Status = st
		j.Error = errMsg
	})
}

type CleanupPolicy struct {
	MaxJobs int
	TTL     time.Duration
}

func (s *Store) Cleanup(policy CleanupPolicy, now time.Time, onDelete func(jobID string)) {
	jobs := s.List()
	sort.Slice(jobs, func(i, k int) bool { return jobs[i].CreatedAt.After(jobs[k].CreatedAt) })

	keep := make(map[string]bool)
	for i, j := range jobs {
		if i < policy.MaxJobs && now.Sub(j.CreatedAt) <= policy.TTL {
			keep[j.ID] = true
		}
	}

	for _, j := range jobs {
		if keep[j.ID] {
			continue
		}
		s.Delete(j.ID)
		if onDelete != nil {
			onDelete(j.ID)
		}
	}
}

