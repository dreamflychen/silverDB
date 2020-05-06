package storage

import (
	"github.com/golang/protobuf/proto"
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"silverDB/compress"
	"silverDB/node/point"
	"silverDB/utils"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const sep=string(os.PathSeparator)

type kv struct {
	mutex     sync.RWMutex
	dataDir   []string
	isCompressed bool
	duration time.Duration
}

func NewKv(dataDir []string,isCompress bool,duration time.Duration) *kv {
	return &kv{
		mutex:        sync.RWMutex{},
		dataDir:      dataDir,
		isCompressed: isCompress,
		duration: duration,
	}
}



type compressPoints struct {
	maxTime int64
	minTime int64
	c compress.Chunk
	fieldKey string
	count int
	tableFile string
}


func (s *kv) writeData (dataBase,table,tagKv string,tags map[string]string,data *metricData) {
	switch s.isCompressed {
	case true:
		chunkList:= s.compressTask(dataBase,table,data)
		for _,c:=range chunkList {
			go func(c *compressPoints) {
				kv:=make(map[int64][]byte)
				vv:=make([]byte,0)
				sMt,_:=time.Unix(c.maxTime,0).MarshalBinary()
				vv=append(vv,sMt...)
				vv=append(vv,c.c.Bytes()...)
				kv[c.minTime]=vv
				e:=setKv(c.tableFile,tagKv,c.fieldKey,kv)
				if e !=nil {
					log.Println("failed persistence data to kv",e)
				}
			}(c)
		}
	case false:
		// 非压缩
		tableFileKv:=s.setTableFile(dataBase,table,data)
		if tableFileKv != nil {
			for tableFile,points:=range tableFileKv {
				go func(points []utils.Point ) {
					value:=make(map[int64][]byte,0)
					for _,p:=range points {
						value[p.T]=utils.Float64ToByte(p.V)
					}
				    e:=setKv(tableFile,tagKv,data.metric,value)
					if e !=nil {
						log.Println("failed persistence data to kv",e)
					}
					}(points)
				}
			}

	}
}


func (s *kv) readTsData(dataBase,tableName,tagKv,fieldKey string,
	tags map[string]string,startTime,endTime int64) [][]byte {
	sTime := strconv.FormatInt(startTime, 10)
	eTime := strconv.FormatInt(endTime, 10)
	tableFileList:=s.getTableFile(dataBase,tableName,sTime,eTime)
	dataSet:=make([][]byte,0)
	var wg sync.WaitGroup
	if tableFileList != nil {
		for k,_:=range tableFileList {
			tableFile:=k
			wg.Add(1)
			go func(wg *sync.WaitGroup) {
				value:=&point.Value {
					Kv: make(map[int64]float64,0),
				}
				if s.isCompressed == true {
					db:=scanCompressKv(tableFile,tagKv,fieldKey,value,startTime,endTime)
					defer db.Close()
				}
				if s.isCompressed ==false {
					db:=scanKv(tableFile,tagKv,fieldKey,value,startTime,endTime)
					defer db.Close()
				}
				buf,e:=proto.Marshal(value)
				if e !=nil {
					log.Println(e.Error())
				}
				dst:=make([]byte,len(buf))
				copy(dst,buf)
				dataSet= append(dataSet, dst)
				wg.Done()
			}(&wg)
			wg.Wait()
		}
	}
	return dataSet
}


func (s *kv) compressTask(dataBase,table string,data *metricData) []*compressPoints {
	var chunkList []*compressPoints
	if data !=nil && len(data.points) !=0 {
		c := s.compress(dataBase,table,data)
		chunkList = append(chunkList,c...)
	}
	return chunkList
}

func (s *kv) compress(dataBase,tableName string,data *metricData) []*compressPoints {
	pointList:=make([]*compressPoints,0)
	tableFileKv:= s.setTableFile(dataBase,tableName,data)
	if tableFileKv != nil {
		for tableFile,points:=range tableFileKv {
			c := compress.NewXORChunk()
			app, err := c.Appender()
			if err != nil {
				log.Println(err)
			}
			if points != nil {
				for _, p := range points {
					app.Append(p.T, p.V)
				}
				count:= len(points)
				p:= &compressPoints{
					maxTime:  points[count-1].T,
					minTime:  points[0].T,
					c:        c,
					fieldKey: data.metric,
					count: count,
					tableFile: tableFile,
				}
				pointList=append(pointList,p)
			}
		}
	}
	return pointList
}


func (s *kv) setTableFile(dataBase,tableName string,data *metricData) map[string][]utils.Point {
	tableFileKv:=make(map[string][]utils.Point,0)
	if data != nil && len(data.points) !=0 {
		minTime:=data.minTime
		maxTime:=data.maxTime
		tableFileMap:=s.scanDataDir(dataBase,tableName,minTime,maxTime)
		if tableFileMap !=nil {
			for tableFile,tr:=range tableFileMap {
				startTime:=tr.startTime
				endTime:=tr.endTime
				index1:=sort.Search(len(data.points), func(i int) bool {
					return data.points[i].T >= startTime
				})
				index2:=sort.Search(len(data.points), func(i int) bool {
					return data.points[i].T < endTime
				})

				if index2 > (len(data.points)) {
					s.mutex.Lock()
					tableFileKv[tableFile]=data.points[index1:]
					s.mutex.Unlock()
					return tableFileKv
				}
				if index1 == index2  && index1==0 {
					s.mutex.Lock()
					tableFileKv[tableFile]=data.points
					s.mutex.Unlock()
					return tableFileKv
				}
				s.mutex.Lock()
				tableFileKv[tableFile]=data.points[index1:index2]
				s.mutex.Unlock()
				return tableFileKv
			}
		}
	}
	return tableFileKv
}

type timeRange struct {
	startTime int64
	endTime int64
}


func (s *kv) spiltTimeRange(minTime,maxTime int64) []*timeRange {

	timeRangeList:=make([]*timeRange,0)

	m:= (maxTime - minTime) / s.duration.Nanoseconds()
	n:= (maxTime - minTime) % s.duration.Nanoseconds()

	if m == 0 && n == 0 {
		timeRange:=&timeRange{
			startTime: 0,
			endTime:   0,
		}
		timeRange.endTime=getEndTime(minTime,s.duration)
		timeRange.startTime=minTime
		timeRangeList=append(timeRangeList,timeRange)
		return timeRangeList
	}

	if m ==0 && n !=0 {
		timeRange:=&timeRange{
			startTime: 0,
			endTime:   0,
		}
		timeRange.endTime=maxTime
		timeRange.startTime=minTime
		timeRangeList=append(timeRangeList,timeRange)
		return timeRangeList
	}

	if m > 0 && n == 0 {
	   k:=int(m)
	   tempMaxTime:=getEndTime(minTime,s.duration)
	   tempMinTime:=minTime
	   for i:=0; i< k ; i++ {
		   timeRange:=&timeRange{
			   startTime: 0,
			   endTime:   0,
		   }
		   timeRange.endTime=tempMaxTime
		   timeRange.startTime=tempMinTime
		   tempMinTime=tempMaxTime
		   tempMaxTime=getEndTime(tempMinTime,s.duration)
		   timeRangeList=append(timeRangeList,timeRange)
	   }
	   return timeRangeList
	}

	if m > 0 && n != 0 {
		k:=int(m)
		tempMaxTime:=getEndTime(minTime,s.duration)
		tempMinTime:=minTime

		for i:=0; i< k ; i++ {
			timeRange:=&timeRange{
				startTime: 0,
				endTime:   0,
			}
			timeRange.endTime=tempMaxTime
			timeRange.startTime=tempMinTime
			tempMinTime=tempMaxTime
			tempMaxTime=getEndTime(tempMinTime,s.duration)
			timeRangeList=append(timeRangeList,timeRange)
		}
		timeRange:=&timeRange{
			startTime: tempMaxTime,
			endTime:   maxTime,
		}
		timeRangeList=append(timeRangeList,timeRange)
		return timeRangeList
	}

	return nil
}


func (s *kv) scanDataDir(dataBase,tableName string,minTime,maxTime int64) map[string]*timeRange {
	timeRangeList:=s.spiltTimeRange(minTime,maxTime)
	tableFileMap:=make(map[string]*timeRange,0)
	if timeRangeList != nil {
		for _,tr:= range timeRangeList {
			var tableFile string
			startTime:= strconv.FormatInt(tr.startTime,10)
			endTime:= strconv.FormatInt(tr.endTime,10)
			dataBaseDir,exist:=s.dataBaseDirIsExist(dataBase)
			if exist == true {
				tableFile=dataBaseDir+sep+tableName+"-"+startTime+"-"+endTime+".db"
			} else {
				rand.Seed(time.Now().UnixNano())
				n := rand.Intn(len(s.dataDir))
				e:=os.MkdirAll(s.dataDir[n]+dataBase,os.ModePerm)
				if e !=nil {
					log.Println("failed create data dir",s.dataDir[n]+dataBase,e)
				}
				tableFile=s.dataDir[n]+dataBase+sep+tableName+"-"+startTime+"-"+endTime+".db"
			}
			ok:=utils.CheckFileIsExist(tableFile)
			if !ok {
				db:=openDB(tableFile)
				e:=db.Close()
				if e !=nil {
					log.Println("failed create tableFile",tableFile,e)
				}
			}
			tableFileMap[tableFile]=tr
		}
		return tableFileMap
	}
    return nil
}

func (s *kv) dataBaseDirIsExist(dataBase string) (string,bool) {
	for _,dir:= range s.dataDir {
		if utils.CheckFileIsExist(dir+dataBase) {
			return dir+dataBase,true
		}
	}
	return "",false
}

func (s *kv) getTableFile(dataBase,tableName,startTime,endTime string) map[string]bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	var tableFile string
	tableFileList:=make(map[string]bool,0)
	dataBaseDir,exist:=s.dataBaseDirIsExist(dataBase)
	if exist == true {
		fileList,err:=ioutil.ReadDir(dataBaseDir)
		if err !=nil {
			log.Println(err)
		}
		if len(fileList) == 0 {
			return nil
		}
		for _,file:=range fileList {
			if !strings.HasSuffix(file.Name(),"lock") {
				tableInfo:=strings.Split(file.Name(),"-")
				tn:=tableInfo[0]
				st:=tableInfo[1]
				et:=strings.Split(tableInfo[2],".")[0]
				if strings.Compare(tn,tableName) == 0 {
					if strings.Compare(startTime,st) >= 0 && strings.Compare(startTime,et) < 0 && strings.Compare(endTime,et) <= 0 {
						tableFile=dataBaseDir+sep+file.Name()
						tableFileList[tableFile]=true
					}
					if strings.Compare(startTime,st) < 0 && strings.Compare(endTime,st) >0 && strings.Compare(endTime,et) < 0 {
						tableFileList1:=s.getTableFile(dataBase,tableName,startTime,st)
						tableFileList2:=s.getTableFile(dataBase,tableName,st,endTime)
						for tableFileK1,tableFileV1:=range tableFileList1 {
							tableFileList[tableFileK1]=tableFileV1
						}
						for tableFileK2,tableFileV2:=range tableFileList2 {
							tableFileList[tableFileK2]=tableFileV2
						}
					}
					if strings.Compare(startTime,st) >= 0 &&  strings.Compare(startTime,et) < 0 && strings.Compare(endTime,et) > 0 {
						tableFileList1:=s.getTableFile(dataBase,tableName,startTime,et)
						tableFileList2:=s.getTableFile(dataBase,tableName,et,endTime)
						for tableFileK1,tableFileV1:=range tableFileList1 {
							tableFileList[tableFileK1]=tableFileV1
						}
						for tableFileK2,tableFileV2:=range tableFileList2 {
							tableFileList[tableFileK2]=tableFileV2
						}
					}
					if strings.Compare(startTime,st) < 0 && strings.Compare(endTime,et) >= 0 {
						tableFileList1:=s.getTableFile(dataBase,tableName,startTime,st)
						tableFileList2:=s.getTableFile(dataBase,tableName,et,endTime)
						tableFile=dataBaseDir+sep+file.Name()
						tableFileList[tableFile]=true
						for tableFileK1,tableFileV1:=range tableFileList1 {
							tableFileList[tableFileK1]=tableFileV1
						}
						for tableFileK2,tableFileV2:=range tableFileList2 {
							tableFileList[tableFileK2]=tableFileV2
						}
					}
				}
			}
		}
	}
	return tableFileList
}
