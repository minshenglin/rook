package longhaul

import (
	"sync"
	"testing"

	"github.com/rook/rook/tests/framework/clients"
	"github.com/rook/rook/tests/framework/contracts"
	"github.com/rook/rook/tests/framework/enums"
	"github.com/rook/rook/tests/framework/installer"
	"github.com/rook/rook/tests/framework/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// Rook Block Storage integration test
// Start MySql database that is using rook provisoned block storage.
// Make sure database is functional

func TestK8sBlockLongHaul(t *testing.T) {
	suite.Run(t, new(K8sBlockLongHaulSuite))
}

type K8sBlockLongHaulSuite struct {
	suite.Suite
	testClient       *clients.TestClient
	bc               contracts.BlockOperator
	kh               *utils.K8sHelper
	initBlockCount   int
	storageClassPath string
	mysqlAppPath     string
	db               *utils.MySQLHelper
	wg               sync.WaitGroup
	installer        *installer.InstallHelper
}

//Test set up - does the following in order
//create pool and storage class, create a PVC, Create a MySQL app/service that uses pvc
func (s *K8sBlockLongHaulSuite) SetupSuite() {

	var err error
	s.kh, err = utils.CreatK8sHelper()
	assert.Nil(s.T(), err)

	s.installer = installer.NewK8sRookhelper(s.kh.Clientset)

	err = s.installer.InstallRookOnK8s()
	require.NoError(s.T(), err)

	s.testClient, err = clients.CreateTestClient(enums.Kubernetes, s.kh)
	require.Nil(s.T(), err)

	s.bc = s.testClient.GetBlockClient()
	initialBlocks, err := s.bc.BlockList()
	require.Nil(s.T(), err)
	s.initBlockCount = len(initialBlocks)
	s.storageClassPath = `apiVersion: rook.io/v1alpha1
kind: Pool
metadata:
  name: {{.poolName}}
  namespace: rook
spec:
  replication:
    size: 1
  # For an erasure-coded pool, comment out the replication count above and uncomment the following settings.
  # Make sure you have enough OSDs to support the replica count or erasure code chunks.
  #erasureCode:
  #  codingChunks: 2
  #  dataChunks: 2
---
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
   name: rook-block
provisioner: rook.io/block
parameters:
    pool: {{.poolName}}`

	s.mysqlAppPath = `apiVersion: v1
kind: Service
metadata:
  name: mysql-app
  labels:
    app: mysqldb
spec:
  ports:
    - port: 3306
      targetPort: 3306
      protocol: TCP
      nodePort: 30003
  selector:
    app: mysqldb
    tier: mysql
  type: NodePort
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: mysql-pv-claim
  labels:
    app: mysqldb
spec:
  storageClassName: rook-block
  accessModes:
  - ReadWriteOnce
  resources:
    requests:
      storage: 20Gi
---
apiVersion: apps/v1beta1
kind: Deployment
metadata:
  name: mysql-app
  labels:
    app: mysqldb
spec:
  strategy:
    type: Recreate
  template:
    metadata:
      labels:
        app: mysqldb
        tier: mysql
    spec:
      containers:
      - image: mysql:5.6
        name: mysql
        env:
        - name: "MYSQL_USER"
          value: "mysql"
        - name: "MYSQL_PASSWORD"
          value: "mysql"
        - name: "MYSQL_DATABASE"
          value: "sample"
        - name: "MYSQL_ROOT_PASSWORD"
          value: "root"
        ports:
        - containerPort: 3306
          name: mysql
        volumeMounts:
        - name: mysql-persistent-storage
          mountPath: /var/lib/mysql
      volumes:
      - name: mysql-persistent-storage
        persistentVolumeClaim:
          claimName: mysql-pv-claim`

	//create storage class
	if scp, _ := s.kh.IsStorageClassPresent("rook-block"); !scp {
		_, err = s.storageClassOperation("mysql-pool", "create")
		require.NoError(s.T(), err)

		//make sure storageclass is created
		present, err := s.kh.IsStorageClassPresent("rook-block")
		require.NoError(s.T(), err)
		require.True(s.T(), present, "Make sure storageclass is present")
	}
	//create mysql pod
	if _, err := s.kh.GetPVCStatus("mysql-pv-claim"); err != nil {

		s.kh.ResourceOperation("create", s.mysqlAppPath)

		//wait till mysql pod is up
		require.True(s.T(), s.kh.IsPodInExpectedState("mysqldb", "", "Running"))
		pvcStatus, err := s.kh.GetPVCStatus("mysql-pv-claim")
		require.Nil(s.T(), err)
		require.Contains(s.T(), pvcStatus, "Bound")
	}
	dbIP, err := s.kh.GetPodHostID("mysqldb", "")
	require.Nil(s.T(), err)
	//create database connection
	s.db = utils.CreateNewMySQLHelper("mysql", "mysql", dbIP+":30003", "sample")

	require.True(s.T(), s.db.PingSuccess())

	if exist := s.db.TableExists(); !exist {
		s.db.CreateTable()
	}

}

func (s *K8sBlockLongHaulSuite) TestBlockLonghaulRun() {

	s.wg.Add(s.installer.Env.LoadConcurrentRuns)
	for i := 1; i <= s.installer.Env.LoadConcurrentRuns; i++ {
		go s.dbOperation(i)
	}
	s.wg.Wait()
}

func (s *K8sBlockLongHaulSuite) dbOperation(i int) {
	defer s.wg.Done()
	//InsertRandomData
	s.db.InsertRandomData()
	s.db.InsertRandomData()
	s.db.InsertRandomData()
	s.db.InsertRandomData()
	s.db.InsertRandomData()
	s.db.InsertRandomData()

	//delete Data
	s.db.DeleteRandomRow()

}
func (s *K8sBlockLongHaulSuite) TearDownSuite() {
	s.db.CloseConnection()
	s.kh.ResourceOperation("delete", s.mysqlAppPath)
	s.storageClassOperation("mysql-pool", "delete")
	s.installer.UninstallRookFromK8s()
	s.testClient = nil
	s.bc = nil
	s.kh = nil
	s.db = nil
	s = nil

}
func (s *K8sBlockLongHaulSuite) storageClassOperation(poolName string, action string) (string, error) {
	config := map[string]string{
		"poolName": poolName,
	}

	result, err := s.kh.ResourceOperationFromTemplate(action, s.storageClassPath, config)

	return result, err

}
