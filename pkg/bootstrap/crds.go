package bootstrap

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sync"

	"k8s.io/client-go/restmapper"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/ghodss/yaml"

	"github.com/maistra/istio-operator/pkg/controller/common"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	"k8s.io/helm/pkg/releaseutil"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

var installCRDsTask sync.Once

// InstallCRDs makes sure all CRDs have been installed.  CRDs are located from
// files in controller.HelmDir/istio-init/files
func InstallCRDs(mgr manager.Manager) error {
	// we only try to install them once.  if there's an error, we should probably
	// panic, as there's no way to recover.  for now, we just pass the error along.
	var err error
	installCRDsTask.Do(func() { internalInstallCRDs(mgr, &err) })
	return err
}

func internalInstallCRDs(mgr manager.Manager, err *error) {
	log.Info("ensuring CRDs have been installed")
	crdPath := path.Join(common.GetHelmDir(), "istio-init/files")
	var crdDir os.FileInfo
	crdDir, *err = os.Stat(crdPath)
	if *err != nil || !crdDir.IsDir() {
		*err = fmt.Errorf("Cannot locate any CRD files in %s", crdPath)
		return
	}
	*err = filepath.Walk(crdPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		return processCRDFile(mgr, path)
	})
}

func processCRDFile(mgr manager.Manager, fileName string) error {
	allErrors := []error{}
	buf := &bytes.Buffer{}
	file, err := os.Open(fileName)
	defer file.Close()
	if err != nil {
		return err
	}
	_, err = buf.ReadFrom(file)
	if err != nil {
		return err
	}
	crdsAdded := false
	k8sClient := mgr.GetClient()
	for index, raw := range releaseutil.SplitManifests(string(buf.Bytes())) {
		rawJSON, err := yaml.YAMLToJSON([]byte(raw))
		if err != nil {
			log.Error(err, "unable to convert raw data to JSON", "file", fileName, "index", index)
			allErrors = append(allErrors, err)
			continue
		}
		obj := &unstructured.Unstructured{}
		_, _, err = unstructured.UnstructuredJSONScheme.Decode(rawJSON, nil, obj)
		if err != nil {
			log.Error(err, "unable to decode object into Unstructured", "file", fileName, "index", index)
			allErrors = append(allErrors, err)
			continue
		}
		gvk := obj.GroupVersionKind()
		if gk := gvk.GroupKind(); gk.String() != "CustomResourceDefinition.apiextensions.k8s.io" {
			continue
		}
		receiver := &unstructured.Unstructured{}
		receiver.SetGroupVersionKind(obj.GroupVersionKind())
		receiver.SetName(obj.GetName())
		err = k8sClient.Get(context.TODO(), client.ObjectKey{Name: obj.GetName()}, receiver)
		if err != nil {
			if errors.IsNotFound(err) {
				log.Info("creating CRD", "file", fileName, "index", index, "CRD", obj.GetName())
				err = k8sClient.Create(context.TODO(), obj)
				if err != nil {
					log.Error(err, "error creating CRD", fileName, "index", index, "CRD", obj.GetName())
					allErrors = append(allErrors, err)
					continue
				}
				crdsAdded = true
			} else {
				allErrors = append(allErrors, err)
				continue
			}
		}
		log.Info("CRD installed", "file", fileName, "index", index, "CRD", obj.GetName())
	}
	if crdsAdded {
		// reset client cache
		mapper := mgr.GetRESTMapper()
		if cachedMapper, ok := mapper.(*restmapper.DeferredDiscoveryRESTMapper); ok {
			cachedMapper.Reset()
		}
	}
	return utilerrors.NewAggregate(allErrors)
}
