package config

import (
	"reflect"
	"testing"
)

func TestConfig_BakedImage(t *testing.T) {
	tests := []struct {
		name string
		c    Config
		repo string
		want string
	}{
		{
			name: "GCP active: returns GCP image",
			c: Config{
				Driver: "gcp-gce",
				GCP: GCP{
					BakedImages: map[string]string{"myrepo": "gcp-image-123"},
				},
				AWS: AWS{
					BakedAMIs: map[string]string{"myrepo": "aws-image-123"},
					BakedAMI:  "legacy-aws",
				},
			},
			repo: "myrepo",
			want: "gcp-image-123",
		},
		{
			name: "AWS Priority: An AWS per-repo image takes precedence over the legacy shared AMI",
			c: Config{
				Driver: "aws-ec2",
				AWS: AWS{
					BakedAMIs: map[string]string{"myrepo": "aws-per-repo-123"},
					BakedAMI:  "legacy-aws-456",
				},
			},
			repo: "myrepo",
			want: "aws-per-repo-123",
		},
		{
			name: "AWS active, per-repo image absent, legacy AMI present: returns legacy AMI",
			c: Config{
				Driver: "aws-ec2",
				AWS: AWS{
					BakedAMIs: map[string]string{"otherrepo": "aws-per-repo-123"},
					BakedAMI:  "legacy-aws-456",
				},
			},
			repo: "myrepo",
			want: "legacy-aws-456",
		},
		{
			name: "Fallback Failure: Requesting an unknown repository returns no image when no legacy fallback exists",
			c: Config{
				Driver: "aws-ec2",
				AWS: AWS{
					BakedAMIs: map[string]string{"otherrepo": "aws-per-repo-123"},
					BakedAMI:  "",
				},
			},
			repo: "myrepo",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.c.BakedImage(tt.repo); got != tt.want {
				t.Errorf("BakedImage() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfig_BakedReplaces(t *testing.T) {
	tests := []struct {
		name string
		c    Config
		repo string
		want []string
	}{
		{
			name: "GCP active: returns slice with GCP image",
			c: Config{
				Driver: "gcp-gce",
				GCP: GCP{
					BakedImages: map[string]string{"myrepo": "gcp-image-123"},
				},
			},
			repo: "myrepo",
			want: []string{"gcp-image-123"},
		},
		{
			name: "BakedReplaces Logic: AWS active, returns slice with per-repo image and legacy AMI",
			c: Config{
				Driver: "aws-ec2",
				AWS: AWS{
					BakedAMIs: map[string]string{"myrepo": "aws-per-repo-123"},
					BakedAMI:  "legacy-aws-456",
				},
			},
			repo: "myrepo",
			want: []string{"aws-per-repo-123", "legacy-aws-456"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.c.BakedReplaces(tt.repo)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("BakedReplaces() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfig_RecordBake(t *testing.T) {
	tests := []struct {
		name      string
		c         Config
		repo      string
		image     string
		wantConfig Config
	}{
		{
			name: "RecordBake Behavior (GCP): initializes nil map and preserves other repositories",
			c: Config{
				Driver: "gcp-gce",
				GCP:    GCP{BakedImages: nil},
			},
			repo:  "myrepo",
			image: "gcp-new-image",
			wantConfig: Config{
				Driver: "gcp-gce",
				GCP: GCP{
					BakedImages: map[string]string{"myrepo": "gcp-new-image"},
				},
			},
		},
		{
			name: "RecordBake Behavior (GCP): preserves existing repository entries",
			c: Config{
				Driver: "gcp-gce",
				GCP: GCP{
					BakedImages: map[string]string{"otherrepo": "old-gcp-image"},
				},
			},
			repo:  "myrepo",
			image: "gcp-new-image",
			wantConfig: Config{
				Driver: "gcp-gce",
				GCP: GCP{
					BakedImages: map[string]string{
						"otherrepo": "old-gcp-image",
						"myrepo":    "gcp-new-image",
					},
				},
			},
		},
		{
			name: "RecordBake Behavior (AWS): initializes nil map and clears legacy field",
			c: Config{
				Driver: "aws-ec2",
				AWS: AWS{
					BakedAMIs: nil,
					BakedAMI:  "legacy-aws-123",
				},
			},
			repo:  "myrepo",
			image: "aws-new-image",
			wantConfig: Config{
				Driver: "aws-ec2",
				AWS: AWS{
					BakedAMIs: map[string]string{"myrepo": "aws-new-image"},
					BakedAMI:  "",
				},
			},
		},
		{
			name: "RecordBake Behavior (AWS): preserves other repositories and clears legacy field",
			c: Config{
				Driver: "aws-ec2",
				AWS: AWS{
					BakedAMIs: map[string]string{"otherrepo": "old-aws-image"},
					BakedAMI:  "legacy-aws-123",
				},
			},
			repo:  "myrepo",
			image: "aws-new-image",
			wantConfig: Config{
				Driver: "aws-ec2",
				AWS: AWS{
					BakedAMIs: map[string]string{
						"otherrepo": "old-aws-image",
						"myrepo":    "aws-new-image",
					},
					BakedAMI: "",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.c.RecordBake(tt.repo, tt.image)
			if !reflect.DeepEqual(tt.c, tt.wantConfig) {
				t.Errorf("RecordBake() resulted in config:\n%v\nwant:\n%v", tt.c, tt.wantConfig)
			}
		})
	}
}

func TestConfig_ClearBakes(t *testing.T) {
	tests := []struct {
		name      string
		c         Config
		wantConfig Config
	}{
		{
			name: "ClearBakes Isolation: GCP active, clears GCP state without wiping AWS state",
			c: Config{
				Driver: "gcp-gce",
				GCP: GCP{
					BakedImages: map[string]string{"myrepo": "gcp-image"},
				},
				AWS: AWS{
					BakedAMIs: map[string]string{"myrepo": "aws-image"},
					BakedAMI:  "legacy-aws",
				},
			},
			wantConfig: Config{
				Driver: "gcp-gce",
				GCP: GCP{
					BakedImages: nil,
				},
				AWS: AWS{
					BakedAMIs: map[string]string{"myrepo": "aws-image"},
					BakedAMI:  "legacy-aws",
				},
			},
		},
		{
			name: "ClearBakes Isolation: AWS active, clears AWS state without wiping GCP state",
			c: Config{
				Driver: "aws-ec2",
				GCP: GCP{
					BakedImages: map[string]string{"myrepo": "gcp-image"},
				},
				AWS: AWS{
					BakedAMIs: map[string]string{"myrepo": "aws-image"},
					BakedAMI:  "legacy-aws",
				},
			},
			wantConfig: Config{
				Driver: "aws-ec2",
				GCP: GCP{
					BakedImages: map[string]string{"myrepo": "gcp-image"},
				},
				AWS: AWS{
					BakedAMIs: nil,
					BakedAMI:  "",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.c.ClearBakes()
			if !reflect.DeepEqual(tt.c, tt.wantConfig) {
				t.Errorf("ClearBakes() resulted in config:\n%v\nwant:\n%v", tt.c, tt.wantConfig)
			}
		})
	}
}
