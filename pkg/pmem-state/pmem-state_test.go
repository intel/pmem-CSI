/*
Copyright 2019  Intel Corporation.

SPDX-License-Identifier: Apache-2.0
*/
package pmemstate_test

import (
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	pmemstate "github.com/intel/pmem-csi/pkg/pmem-state"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type testData struct {
	Id     string
	Name   string
	Params map[string]string
}

func (td *testData) IsEqual(other testData) bool {
	if other.Id != td.Id {
		return false
	}
	if other.Name != td.Name {
		return false
	}

	if len(td.Params) != len(other.Params) {
		return false
	}

	for k, v := range td.Params {
		otherV, ok := other.Params[k]
		if !ok || v != otherV {
			return false
		}
	}

	return true
}

func TestPmemState(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "PMEM State Suite")
}

var _ = Describe("pmem state", func() {
	var stateDir string
	var DontClean bool = false

	BeforeEach(func() {
		var err error
		stateDir, err = os.MkdirTemp("", "pmemstate-")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		if !DontClean {
			os.RemoveAll(stateDir)
		}
	})

	Context("State API", func() {
		It("new file state", func() {
			data := testData{
				Id:   "id1",
				Name: "test-data",
				Params: map[string]string{
					"key1": "val1",
					"key2": "val2",
				},
			}
			rData := testData{}
			var err error

			_, err = pmemstate.NewFileState("")
			Expect(err).To(HaveOccurred())

			_, err = pmemstate.NewFileState("/unknown/base/directory/")
			Expect(err).To(HaveOccurred())

			file, err := os.CreateTemp("", "pmemstate-file")
			Expect(err).NotTo(HaveOccurred())
			_, err = pmemstate.NewFileState(file.Name())
			os.Remove(file.Name()) //nolint: errcheck
			Expect(err).To(HaveOccurred())

			Expect(stateDir).ShouldNot(BeNil())
			fs, err := pmemstate.NewFileState(stateDir)
			Expect(err).NotTo(HaveOccurred())

			err = fs.Create(data.Id, data)
			Expect(err).NotTo(HaveOccurred())

			err = fs.Get(data.Id, &rData)
			Expect(err).NotTo(HaveOccurred())

			Expect(data.IsEqual(rData)).To(Equal(true))
		})

		It("multiple files", func() {
			data := []testData{
				testData{
					Id:   "one",
					Name: "test-data1",
					Params: map[string]string{
						"key1": "val1",
						"key2": "val2",
					},
				},
				testData{
					Id:   "two",
					Name: "test-data2",
					Params: map[string]string{
						"key1": "val1",
						"key2": "val2",
					},
				},
				testData{
					Id:   "three",
					Name: "test-data3",
					Params: map[string]string{
						"key1": "val1",
						"key2": "val2",
					},
				},
			}
			var err error

			Expect(stateDir).ShouldNot(BeNil())
			fs, err := pmemstate.NewFileState(stateDir)
			Expect(err).NotTo(HaveOccurred())

			for _, d := range data {
				err = fs.Create(d.Id, d)
				Expect(err).NotTo(HaveOccurred())
			}

			for _, d := range data {
				rData := testData{}
				err = fs.Get(d.Id, &rData)
				Expect(err).NotTo(HaveOccurred())
				Expect(d.IsEqual(rData)).To(Equal(true))
			}

			// Delete and GetAll tests
			ids, err := fs.GetAll()
			Expect(err).NotTo(HaveOccurred(), "retrieve records")
			for _, id := range ids {
				found := false
				for _, d := range data {
					if d.Id == id {
						found = true
						rData := testData{}
						err := fs.Get(id, &rData)
						Expect(err).NotTo(HaveOccurred(), "read record: %s", id)
						Expect(d.IsEqual(rData)).To(Equal(true))

						err = fs.Delete(id)
						Expect(err).NotTo(HaveOccurred())
						break
					}
				}
				Expect(found).To(Equal(true))
			}
			Expect(err).NotTo(HaveOccurred())

			// Should have left no file
			ids, err = fs.GetAll()
			Expect(err).NotTo(HaveOccurred(), "retrieve records")
			Expect(len(ids)).Should(BeZero(), "all records should have been deleted")
		})

		It("read write files", func() {
			data := []testData{
				testData{
					Id:   "one",
					Name: "test-data1",
					Params: map[string]string{
						"key1": "val1",
						"key2": "val2",
					},
				},
				testData{
					Id:   "two",
					Name: "test-data2",
					Params: map[string]string{
						"key1": "val1",
						"key2": "val2",
					},
				},
				testData{
					Id:   "three",
					Name: "test-data3",
					Params: map[string]string{
						"key1": "val1",
						"key2": "val2",
					},
				},
			}
			var err error

			Expect(stateDir).ShouldNot(BeNil())
			fs, err := pmemstate.NewFileState(stateDir)
			Expect(err).NotTo(HaveOccurred())

			for _, d := range data {
				err = fs.Create(d.Id, d)
				Expect(err).NotTo(HaveOccurred())
			}

			for _, d := range data {
				rData := testData{}
				err = fs.Get(d.Id, &rData)
				Expect(err).NotTo(HaveOccurred())
				Expect(d.IsEqual(rData)).To(Equal(true))
			}
		})

		It("concurrent read writes", func() {
			data := []testData{
				testData{
					Id:   "one",
					Name: "test-data1",
					Params: map[string]string{
						"key1": "val1",
						"key2": "val2",
					},
				},
				testData{
					Id:   "two",
					Name: "test-data2",
					Params: map[string]string{
						"key1": "val1",
						"key2": "val2",
					},
				},
				testData{
					Id:   "three",
					Name: "test-data3",
					Params: map[string]string{
						"key1": "val1",
						"key2": "val2",
					},
				},
			}

			Expect(stateDir).ShouldNot(BeNil())
			fs, err := pmemstate.NewFileState(stateDir)
			Expect(err).NotTo(HaveOccurred())

			wg := sync.WaitGroup{}
			for _, d := range data {
				wg.Add(1)
				// Writer
				go func(fs_ pmemstate.StateManager, d_ testData) {
					var createError error
					fmt.Printf(">> %s\n", d_.Id)
					createError = fs_.Create(d_.Id, d_)
					Expect(createError).NotTo(HaveOccurred())
					wg.Done()
				}(fs, d)

				wg.Add(1)
				// Reader
				go func(fs_ pmemstate.StateManager, d_ testData) {
					var getErr error
					rData := testData{}
					fmt.Printf("<< %s\n", d_.Id)
					i := 0
					for i < 10 {
						getErr = fs_.Get(d_.Id, &rData)
						if getErr == nil {
							break
						}
						if strings.HasSuffix(getErr.Error(), "no such file or directory") {
							// Might not ready yet, try again
							i++
							fmt.Printf("<< File '%s' is not ready yet, will retry(%d)\n", d_.Id, i)
							time.Sleep(200 * time.Millisecond)
						} else {
							break
						}
					}
					Expect(getErr).NotTo(HaveOccurred())
					Expect(d_.IsEqual(rData)).To(Equal(true))
					wg.Done()
				}(fs, d)
			}
			wg.Wait()
		})

		It("corrupted file", func() {
			data := testData{
				Id:   "one",
				Name: "test-data1",
				Params: map[string]string{
					"key1": "val1",
					"key2": "val2",
				},
			}
			var err error

			Expect(stateDir).ShouldNot(BeNil())
			fs, err := pmemstate.NewFileState(stateDir)
			Expect(err).NotTo(HaveOccurred())

			err = fs.Create(data.Id, data)
			Expect(err).NotTo(HaveOccurred())

			// truncate file data
			file := path.Join(stateDir, data.Id+".json")
			fInfo, err := os.Stat(file)
			Expect(err).NotTo(HaveOccurred())
			err = os.Truncate(file, fInfo.Size()-10)
			Expect(err).NotTo(HaveOccurred())

			rData := testData{}
			err = fs.Get(data.Id, &rData)
			Expect(err).To(HaveOccurred())
		})

		It("able to read/write with different parameters", func() {
			data := []testData{
				testData{
					Id:   "one",
					Name: "with multiple params",
					Params: map[string]string{
						"key1": "val1",
						"key2": "val2",
					},
				},
				testData{
					Id:   "two",
					Name: "with single params",
					Params: map[string]string{
						"key1": "val1",
					},
				},
				testData{
					Id:     "three",
					Name:   "with empty params",
					Params: map[string]string{},
				},
			}
			var err error

			Expect(stateDir).ShouldNot(BeNil())
			fs, err := pmemstate.NewFileState(stateDir)
			Expect(err).NotTo(HaveOccurred())

			for _, d := range data {
				err = fs.Create(d.Id, d)
				Expect(err).NotTo(HaveOccurred())
			}

			ids, err := fs.GetAll()
			Expect(err).ShouldNot(HaveOccurred(), "read all records")
			Expect(len(ids)).Should(BeEquivalentTo(len(data)), "record count should match")

			for _, id := range ids {
				rData := testData{}
				err = fs.Get(id, &rData)
				Expect(err).NotTo(HaveOccurred())
				Expect(data).Should(ContainElement(rData), "records data shold match")
			}
		})
	})
})
