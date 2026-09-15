//
// Created by peng on 2021/9/10.
//

#ifndef NEU_BLOCKCHAIN_EV_CONSENSUS_MANAGER_H
#define NEU_BLOCKCHAIN_EV_CONSENSUS_MANAGER_H

#include "raft/consensus_manager.h"

class EVConsensusManager : public ConsensusManager<EVConsensusManager> {
public:
    EVConsensusManager(const consensus_param &param, brpc::Server* server);

    // Base ConsensusManager starts receiver_from_user() during its constructor,
    // before this derived class has assigned duration / request_receiver_port.
    // Set these before constructing EVConsensusManager.
    static void setBootParams(double durationSec, int receiverPort);

    // CRTP: these method is called by base class
    // create a thread for receiving message from raft cluster
    void receiver_from_raft();

    // create a thread to send message to raft cluster
    void receiver_from_user();

private:
    static double bootDuration;
    static int bootReceiverPort;

    double duration;
    size_t request_receiver_port;
    size_t server_count;
    std::atomic<size_t> current_epoch = 1;
};


#endif //NEU_BLOCKCHAIN_EV_CONSENSUS_MANAGER_H
