#ifndef NEUBLOCKCHAIN_WORKLOAD_SIZE_ADAPTER_H
#define NEUBLOCKCHAIN_WORKLOAD_SIZE_ADAPTER_H

#include <atomic>
#include <string>
#include <vector>
#include "common/aria_types.h"
#include "glog/logging.h"
#include <unistd.h>

class WorkloadSizeAdapter {
public:
    explicit WorkloadSizeAdapter(size_t _aggregateServerCount=1) {
        aggregateServerCount = _aggregateServerCount;
        cacheEpoch = 1;
        cacheCountList.resize(cacheSize);
        for (auto& cnt : cacheCountList) {
            cnt = 3000;
        }

        coreCount = sysconf(_SC_NPROCESSORS_ONLN) / 2 + 1;
        minTxCount = recommendMinTx;
        maxTxCount = recommendMaxTx;
    }

    inline void setAggregateServerCount(size_t _aggregateServerCount) {
        aggregateServerCount = _aggregateServerCount;
    }

    inline void setLastEpochTxCount(epoch_size_t epoch, size_t count) {
        if(cacheEpoch == epoch) {
            cacheCountList[epoch % cacheSize] += count;
        } else if (epoch == cacheEpoch + 1) {
            DLOG(INFO) << "lastEpoch: " << cacheEpoch << ", txn size: " << cacheCountList[epoch % cacheSize];
            calculateBestWorkloadSize();
            cacheEpoch = epoch;
            cacheCountList[epoch % cacheSize] = count;
        } else if (epoch > cacheEpoch + 1) {
            LOG(WARNING) << "epoch gap in workload adapter: cacheEpoch=" << cacheEpoch
                         << " got=" << epoch;
            calculateBestWorkloadSize();
            cacheEpoch = epoch;
            cacheCountList[epoch % cacheSize] = count;
        } else {
            cacheCountList[epoch % cacheSize] += count;
        }
    }

    [[nodiscard]] inline size_t getMinTxPerWorkload() const {
        return minTxCount;
    }

    [[nodiscard]] inline size_t getMaxTxPerWorkload() const {
        return maxTxCount;
    }

    [[nodiscard]] inline size_t getMaxBufferSize() const {
        return recommendMaxBuffer;
    }

protected:
    inline void calculateBestWorkloadSize() {
        double averageCnt = 0;
        int zeros = 0;
        for (auto& cnt : cacheCountList) {
            if (cnt == 0) {
                zeros++;
            }
            averageCnt += double(cnt);
        }
        averageCnt /= cacheSize-zeros;

        auto minFactor = averageCnt / (static_cast<double>(aggregateServerCount * coreCount * recommendMaxTx)) + 1;
        auto maxFactor = averageCnt / (static_cast<double>(aggregateServerCount * coreCount * recommendMinTx)) + 1;
        auto minTxTmpCount = averageCnt / (coreCount * maxFactor);
        auto maxTxTmpCount = averageCnt / (coreCount * minFactor);
        minTxTmpCount = minTxTmpCount < minTxThreadHold ? minTxThreadHold : minTxTmpCount;
        minTxCount = minTxTmpCount;
        maxTxCount = maxTxTmpCount < minTxTmpCount ? minTxTmpCount : maxTxTmpCount;
    }

private:
    epoch_size_t cacheEpoch;
    const int cacheSize = 5;
    std::vector<size_t> cacheCountList;

private:
    size_t aggregateServerCount;
    size_t coreCount;
    time_t lastEpochDuration{};
    volatile size_t maxTxCount{};
    volatile size_t minTxCount{};

public:
    const size_t recommendMaxTx = 1000;
    const size_t recommendMinTx = 500;
    const size_t recommendMaxBuffer = 5000;
    const size_t minTxThreadHold = 5;
};


#endif
