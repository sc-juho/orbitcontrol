package containrunner

import "encoding/json"
import "github.com/fsouza/go-dockerclient"
import "fmt"
import "strings"
import "os"
import "net"
import "regexp"

type ContainerCheck struct {
	Type             string
	Url              string
	DummyResult      bool
	ExpectHttpStatus string
	ExpectString     string
}

type ContainerConfiguration struct {
	Name       string
	HostConfig docker.HostConfig
	Config     docker.Config
	Checks     []ContainerCheck
}

type MachineConfiguration struct {
	Containers         map[string]ContainerConfiguration `json:"containers"`
	AuthoritativeNames []string                          `json:"authoritative_names"`
}

type ContainerDetails struct {
	docker.APIContainers
	Container *docker.Container
}

func GetServiceConfigurationString() string {
	return ""
}

func GetConfiguration(str string) MachineConfiguration {
	var conf MachineConfiguration
	err := json.Unmarshal([]byte(str), &conf)
	if err != nil {
		panic(err)
	}

	return conf
}

func GetDockerClient() *docker.Client {
	endpoint := "unix:///var/run/docker.sock"
	client, err := docker.NewClient(endpoint)
	if err != nil {
		panic(err)
	}
	return client
}

func FindMatchingContainers(existing_containers []ContainerDetails, required_container ContainerConfiguration) (found_containers []ContainerDetails, remaining_containers []ContainerDetails) {

	for _, container_details := range existing_containers {

		found := true
		if container_details.Container.Config.Image != required_container.Config.Image {
			remaining_containers = append(remaining_containers, container_details)
			continue
		}
		if required_container.Config.Hostname != "" && container_details.Container.Config.Hostname != required_container.Config.Hostname {
			remaining_containers = append(remaining_containers, container_details)
			continue
		}
		if container_details.Container.Name != required_container.Name {
			remaining_containers = append(remaining_containers, container_details)
			continue
		}

		/* TBD
		if required_container.DockerOptions["Env"] != nil {
			envs := required_container.DockerOptions["Env"].([]interface{})
			for _, env := range envs {
				fmt.Println("env ", env.(string), "\n")
			}
		}
		*/

		if found {
			found_containers = append(found_containers, container_details)
			//fmt.Println("Found matching!", container_details)
		} else {
			remaining_containers = append(remaining_containers, container_details)
		}
	}

	return found_containers, remaining_containers
}

func ConvergeContainers(conf MachineConfiguration, client *docker.Client) {
	var opts docker.ListContainersOptions
	var ready_for_launch []ContainerConfiguration
	opts.All = true
	existing_containers_info, err := client.ListContainers(opts)
	if err != nil {
		panic(err)
	}

	var existing_containers []ContainerDetails
	for _, container_info := range existing_containers_info {
		container := ContainerDetails{APIContainers: container_info}
		container.Container, err = client.InspectContainer(container.ID)
		if err != nil {
			panic(err)
		}

		// For some reason the container name has / prefix (eg. "/comet"). Strip it out
		if container.Container.Name[0] == '/' {
			container.Container.Name = container.Container.Name[1:]
		}

		existing_containers = append(existing_containers, container)
	}

	var matching_containers []ContainerDetails
	for _, required_container := range conf.Containers {
		matching_containers, existing_containers = FindMatchingContainers(existing_containers, required_container)

		if len(matching_containers) > 1 {
			fmt.Println("Weird! Found more than one container matching specs: ", matching_containers)
		}

		if len(matching_containers) == 0 {
			fmt.Println("No containers found matching ", required_container, ". Marking for launch...")
			ready_for_launch = append(ready_for_launch, required_container)
		}

		if len(matching_containers) == 1 {
			if matching_containers[0].Container.State.Running {
				fmt.Println("Found one matching container and it's running")
			} else {
				fmt.Println("Found one matching container and it's not running!", matching_containers[0])
			}

		}
	}

	fmt.Println("Remaining running containers: ", len(existing_containers))
	var imageRegexp = regexp.MustCompile("(.+):")
	for _, container := range existing_containers {
		m := imageRegexp.FindStringSubmatch(container.Image)
		image := m[1]

		for _, authoritative_name := range conf.AuthoritativeNames {
			if authoritative_name == image {
				fmt.Printf("Found container %+v which we are authoritative but its running. Going to stop it...\n", container)
				client.StopContainer(container.Container.ID, 10)
				err = client.RemoveContainer(docker.RemoveContainerOptions{container.Container.ID, true, true})
				if err != nil {
					panic(err)
				}
			}
		}

	}

	for _, container := range ready_for_launch {
		LaunchContainer(container, client)
	}

}

func LaunchContainer(containerConfiguration ContainerConfiguration, client *docker.Client) {

	// Check if we need to pull the image first
	image, err := client.InspectImage(containerConfiguration.Config.Image)
	if err != nil && err != docker.ErrNoSuchImage {
		panic(err)
	}

	if image == nil {
		fmt.Println("Need to pull image", containerConfiguration.Config.Image)
		var pullImageOptions docker.PullImageOptions
		pullImageOptions.Registry = containerConfiguration.Config.Image[0:strings.Index(containerConfiguration.Config.Image, "/")]
		imagePlusTag := containerConfiguration.Config.Image[strings.Index(containerConfiguration.Config.Image, "/")+1:]
		pullImageOptions.Repository = pullImageOptions.Registry + "/" + imagePlusTag[0:strings.Index(imagePlusTag, ":")]
		pullImageOptions.Tag = imagePlusTag[strings.Index(imagePlusTag, ":")+1:]
		pullImageOptions.OutputStream = os.Stderr

		fmt.Printf("PullImageOptions %+v\n", pullImageOptions)
		ret := client.PullImage(pullImageOptions, docker.AuthConfiguration{})
		fmt.Println("Ret:", ret)
	}

	var options docker.CreateContainerOptions
	options.Name = containerConfiguration.Name
	options.Config = &containerConfiguration.Config

	var addresses []string
	addresses, err = net.LookupHost("skydns.services.dev.docker")
	if err == nil {
		containerConfiguration.HostConfig.Dns = []string{addresses[0]}
		containerConfiguration.HostConfig.DnsSearch = []string{"services.dev.docker"}
	}

	fmt.Println("Creating container", options)
	container, err := client.CreateContainer(options)
	if err != nil {
		panic(err)
	}

	err = client.StartContainer(container.ID, &containerConfiguration.HostConfig)
	if err != nil {
		panic(err)
	}

}
