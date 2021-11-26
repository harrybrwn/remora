package deploy

type Instance struct {
	Host     string   `yaml:"host"`
	Image    string   `yaml:"image"`
	Name     string   `yaml:"name"`
	Volumes  []string `yaml:"volumes"`
	Command  []string `yaml:"command"`
	Replicas int      `yaml:"replicas"`
	Build    *Build   `yaml:"build"`
}

type ConfigHost struct {
	Host      string     `yaml:"host"`
	Image     string     `yaml:"image"`
	Command   []string   `yaml:"command"`
	Volumes   []string   `yaml:"volumes"`
	Instances []Instance `yaml:"instances"`
}

type Build struct {
	Host       string `yaml:"host"`
	Image      string `yaml:"image"`
	Context    string `yaml:"context"`
	Dockerfile string `yaml:"dockerfile"`
}

func (b *Build) InitDefaults() {
	if b.Context == "" {
		b.Context = "."
	}
	if b.Dockerfile == "" {
		b.Dockerfile = "./Dockerfile"
	}
}

type UploadConfig struct {
	Volume string   `yaml:"volume"`
	Files  []string `yaml:"files"`
}

type HostConfig struct {
	Host      string
	Builds    []Build
	Instances []Instance
}
